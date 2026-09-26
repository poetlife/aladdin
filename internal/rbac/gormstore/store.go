package gormstore

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/database/migrate"
	"github.com/poetlife/aladdin/internal/rbac"
)

// Store 是 rbac.Store 与 rbac.MutableStore 的关系库实现。
//
// 它持有的是连接而不是配置：方言解析、连接串归一、连接池取值都在
// internal/database 完成，本包只消费一个已打开的 *gorm.DB。
type Store struct {
	db *gorm.DB
}

// New 用已打开的连接构造存储。
//
// 它不迁移、不初始化内置角色：那些是通过 Open 走的启动路径，而 New 让
// 测试可以直接接一个已经准备好的连接（例如内存库）。
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Open 按配置打开持久化存储，返回可直接交给服务端的存储。
//
// 它把"连接 → 迁移 → 补内置角色"这个顺序**固化在一处**。这三步的先后
// 是有意义的：没有连接谈不上迁移，没有表写不进内置角色，而没有内置角色
// 判定就无从谈起。分散在入口进程里，迟早会有人写反顺序，而写反的表现是
// 服务能起来、却对谁都拒绝——一次看起来像权限配置错误的事故。
//
// 任何一步失败都返回错误且不留下半个可用的存储：服务端必须拒绝启动
// （见 docs/design/persistence/README.md）。
func Open(ctx context.Context, cfg config.DatabaseConfig, logger *zap.Logger) (*Store, error) {
	db, err := database.Open(cfg, logger)
	if err != nil {
		return nil, err
	}
	if err := migrate.Run(ctx, db, logger); err != nil {
		closeQuietly(db)
		return nil, err
	}

	store := New(db)
	created, err := rbac.EnsureBuiltinRoles(ctx, store)
	if err != nil {
		closeQuietly(db)
		return nil, err
	}
	if created > 0 {
		logger.Info("已补入缺失的内置角色", zap.Int("count", created))
	}
	return store, nil
}

// DB 返回底层连接，供入口进程装配**其它模块**的存储实现。
//
// 它是一个装配用的接缝，不是查询入口：连接只在这里打开一次、迁移只在这里
// 推进一次，而认证模块的会话表与身份别名表由同一份迁移建好，因此它们的
// 存储实现必须复用这一条连接。业务代码不得用它绕开各模块自己的存储抽象
// 去直接查库（见 docs/ssot-registry.md 的数据源类）。
func (s *Store) DB() *gorm.DB { return s.db }

// Close 释放连接池。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("获取数据库连接池失败: %w", err)
	}
	return sqlDB.Close()
}

// closeQuietly 在构造失败时释放已经打开的连接。
//
// 错误忽略是刻意的：真正的失败原因要原样返回给调用方，关连接没关干净
// 只会给启动失败的告警多裹一句无关的话，反而盖住重点。
func closeQuietly(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// Role 实现 rbac.Store。
func (s *Store) Role(ctx context.Context, roleID string) (rbac.RoleDefinition, error) {
	var rec database.RoleRecord
	if err := s.db.WithContext(ctx).First(&rec, "id = ?", roleID).Error; err != nil {
		return rbac.RoleDefinition{}, roleLookupError(err, roleID)
	}
	return toRoleDefinition(rec), nil
}

// Roles 实现 rbac.Store。
//
// 不带 ORDER BY：顺序由 rbac.SortRoles 定义，两个存储实现共用同一条规则。
// 让 SQL 也排一次，就是给同一件事写了第二个实现。
func (s *Store) Roles(ctx context.Context) ([]rbac.RoleDefinition, error) {
	var recs []database.RoleRecord
	if err := s.db.WithContext(ctx).Find(&recs).Error; err != nil {
		return nil, unavailable("读取角色列表", err)
	}
	out := make([]rbac.RoleDefinition, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toRoleDefinition(rec))
	}
	return rbac.SortRoles(out), nil
}

// SubjectBindings 实现 rbac.Store。
//
// 先确认主体登记过：未登记的主体与"登记过但没有任何角色"是两回事，
// 前者是 ErrSubjectNotFound，后者是一个空列表。判定路径靠这个区分
// 决定是报"主体不存在"还是"确实没有这条权限"。
func (s *Store) SubjectBindings(ctx context.Context, subjectID string) ([]rbac.RoleBinding, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&database.SubjectRecord{}).
		Where("id = ?", subjectID).Count(&count).Error
	if err != nil {
		return nil, subjectLookupError(err, subjectID)
	}
	if count == 0 {
		return nil, fmt.Errorf("%w: %s", rbac.ErrSubjectNotFound, subjectID)
	}

	var recs []database.RoleBindingRecord
	if err := s.db.WithContext(ctx).Where("subject_id = ?", subjectID).Find(&recs).Error; err != nil {
		return nil, unavailable("读取主体 "+subjectID+" 的角色绑定", err)
	}
	return rbac.SortBindings(toBindings(recs)), nil
}

// Subject 实现 rbac.Store。
//
// 只有签发会话的路径读它：会话要把主体在**签发那一刻**的类型与默认作用域
// 冻结下来，此后校验只读会话表，不再回头查主体。因此这不是判定路径上的
// 一次查询，而是"登记之后读回当前值"的入口。
func (s *Store) Subject(ctx context.Context, subjectID string) (rbac.Subject, error) {
	var rec database.SubjectRecord
	if err := s.db.WithContext(ctx).First(&rec, "id = ?", subjectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return rbac.Subject{}, fmt.Errorf("%w: %s", rbac.ErrSubjectNotFound, subjectID)
		}
		return rbac.Subject{}, subjectLookupError(err, subjectID)
	}
	return toSubject(rec), nil
}

// PutRole 实现 rbac.MutableStore。
//
// 冲突时覆盖全部非主键列，即"写入或覆盖"。覆盖而非报错，是因为角色定义
// 从来是整份读写的，调用方拿到的就是它想要的最终状态。
func (s *Store) PutRole(ctx context.Context, role rbac.RoleDefinition) error {
	rec := fromRoleDefinition(role)
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入角色 "+role.ID, err)
	}
	return nil
}

// DeleteRole 实现 rbac.MutableStore。
//
// 只执行、不判定：内置角色不可删除、角色是否仍被占用都由调用方先经
// rbac.ValidateRoleDeletion 校验。删除不存在的角色返回 ErrRoleNotFound，
// 而不是静默成功——多半意味着调用方拼错了角色标识。
func (s *Store) DeleteRole(ctx context.Context, roleID string) error {
	result := s.db.WithContext(ctx).Where("id = ?", roleID).Delete(&database.RoleRecord{})
	if result.Error != nil {
		return unavailable("删除角色 "+roleID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", rbac.ErrRoleNotFound, roleID)
	}
	return nil
}

// PutSubject 实现 rbac.MutableStore。
func (s *Store) PutSubject(ctx context.Context, subject rbac.Subject) error {
	rec := fromSubject(subject)
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入主体 "+subject.ID, err)
	}
	return nil
}

// Bind 实现 rbac.MutableStore。
//
// 幂等由库保证：三列复合主键 + 冲突即忽略，因此重复授予不会产生第二条
// 记录。"写入前先查一下"做不到这一点——并发下两次查询都可以说"不存在"。
func (s *Store) Bind(ctx context.Context, binding rbac.RoleBinding) error {
	rec := fromBinding(binding)
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rec).Error
	if err != nil {
		return unavailable("写入绑定 "+binding.SubjectID+"/"+binding.RoleID, err)
	}
	return nil
}

// Unbind 实现 rbac.MutableStore。
//
// 删除零行不是错误：重复撤销与撤销一个本来就没有的绑定，结果一致。
func (s *Store) Unbind(ctx context.Context, binding rbac.RoleBinding) error {
	result := s.db.WithContext(ctx).
		Where("subject_id = ? AND role_id = ? AND scope = ?",
			binding.SubjectID, binding.RoleID, string(binding.Scope)).
		Delete(&database.RoleBindingRecord{})
	if result.Error != nil {
		return unavailable("撤销绑定 "+binding.SubjectID+"/"+binding.RoleID, result.Error)
	}
	return nil
}

// BindingsOfRole 实现 rbac.MutableStore。
func (s *Store) BindingsOfRole(ctx context.Context, roleID string) ([]rbac.RoleBinding, error) {
	var recs []database.RoleBindingRecord
	if err := s.db.WithContext(ctx).Where("role_id = ?", roleID).Find(&recs).Error; err != nil {
		return nil, unavailable("读取角色 "+roleID+" 的绑定", err)
	}
	return rbac.SortBindings(toBindings(recs)), nil
}

// 编译期断言：本实现同时满足两个接口。少实现一个方法时在这里就报错，
// 而不是等到服务端装配处才报——那时的错误信息指向装配代码，不指向这里。
var (
	_ rbac.Store        = (*Store)(nil)
	_ rbac.MutableStore = (*Store)(nil)
)
