package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/registration"
)

// policyRowID 是注册策略那一行固定的主键取值。
//
// 它是一个**站点级**的取值，不是一个标识：写死在这里，是为了让"这张表只有一行"
// 成为库层面的事实，而不是靠"读的时候取第一行"这件事来维持。
const policyRowID = "site"

// Store 是 registration.Store 的关系库实现。
type Store struct {
	db *gorm.DB
}

// New 用已打开的连接构造存储。
//
// 它不迁移：表由启动路径上的迁移建好（见 internal/database/migrate），本构造
// 函数只负责把记录类型与领域类型对上。
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Policy 实现 registration.Store。
//
// **没有记录时返回零值**，不在这里补一个默认策略：取值缺失等价于开放注册这条
// 归一化只有一处（registration.Policy.Effective），两个实现各补一次就会各有一套。
func (s *Store) Policy(ctx context.Context) (registration.Policy, error) {
	var rec database.RegistrationPolicyRecord
	err := s.db.WithContext(ctx).First(&rec, "id = ?", policyRowID).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return registration.Policy{}, nil
	case err != nil:
		return registration.Policy{}, unavailable("读取注册策略", err)
	}
	return toPolicy(rec), nil
}

// PutPolicy 实现 registration.Store。
func (s *Store) PutPolicy(ctx context.Context, policy registration.Policy) error {
	rec := fromPolicy(policy)
	rec.ID = policyRowID
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入注册策略", err)
	}
	return nil
}

// Invites 实现 registration.Store。
//
// 它**不排序**：顺序由领域层统一决定（registration.sortInvites），两个实现各排
// 一次会给"列表顺序"两个来源。
func (s *Store) Invites(ctx context.Context) ([]registration.Invite, error) {
	var recs []database.RegistrationInviteRecord
	if err := s.db.WithContext(ctx).Find(&recs).Error; err != nil {
		return nil, unavailable("读取邀请码", err)
	}
	out := make([]registration.Invite, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toInvite(rec))
	}
	return out, nil
}

// PutInvite 实现 registration.Store。
func (s *Store) PutInvite(ctx context.Context, invite registration.Invite) error {
	rec := fromInvite(invite)
	if err := s.db.WithContext(ctx).Create(&rec).Error; err != nil {
		return unavailable("写入邀请码", err)
	}
	return nil
}

// RevokeInvite 实现 registration.Store。
//
// 撤销条件带上 `revoked_at IS NULL`：重复撤销**不覆盖**第一次的时间戳。第一次
// 撤销的时刻才是"这个码什么时候失效的"这个问题的答案，后一次点击不该改写它；
// 而两次都返回成功，是因为重复点击不该变成一个调用方无法与真失败区分开的错误。
func (s *Store) RevokeInvite(ctx context.Context, id string, at time.Time) (registration.Invite, error) {
	res := s.db.WithContext(ctx).
		Model(&database.RegistrationInviteRecord{}).
		Where("id = ? AND revoked_at IS NULL", id).
		UpdateColumn("revoked_at", at)
	if res.Error != nil {
		return registration.Invite{}, unavailable("撤销邀请码", res.Error)
	}

	// 无论上面有没有改动到行，都读回现状：行不存在与已撤销都要能被区分出来
	// （前者是 ErrInviteNotFound，后者是幂等成功）。
	var rec database.RegistrationInviteRecord
	err := s.db.WithContext(ctx).First(&rec, "id = ?", id).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return registration.Invite{}, registration.ErrInviteNotFound
	case err != nil:
		return registration.Invite{}, unavailable("读回邀请码", err)
	}
	return toInvite(rec), nil
}

// Redeem 实现 registration.Store。
//
// **判断与写入是一次操作。** 把"还剩不剩次数""过没过期""撤没撤销"三件事一起放进
// WHERE，由这一次条件更新同时回答与推进：行数为 1 就是兑换成功且已经扣掉了这一次，
// 为 0 就是兑换不动。先查后写在并发下会超发——两个请求可以同时读到"还剩一次"。
//
// 行数为 0 时**不区分**是哪一条条件不成立，也不回头再查一次：对调用方它们本来
// 就是同一件事，而多查一次只是给探测者多一个可观察的差异。
func (s *Store) Redeem(ctx context.Context, codeHash string, at time.Time) (registration.Invite, error) {
	res := s.db.WithContext(ctx).
		Model(&database.RegistrationInviteRecord{}).
		Where("code_hash = ?", codeHash).
		Where("revoked_at IS NULL").
		Where("expires_at IS NULL OR expires_at > ?", at).
		Where("max_uses = 0 OR used_count < max_uses").
		UpdateColumn("used_count", gorm.Expr("used_count + 1"))
	if res.Error != nil {
		return registration.Invite{}, unavailable("兑换邀请码", res.Error)
	}
	if res.RowsAffected == 0 {
		return registration.Invite{}, registration.ErrInviteUnusable
	}

	var rec database.RegistrationInviteRecord
	if err := s.db.WithContext(ctx).First(&rec, "code_hash = ?", codeHash).Error; err != nil {
		return registration.Invite{}, unavailable("读回邀请码", err)
	}
	return toInvite(rec), nil
}

// toPolicy 把记录翻成领域取值。
//
// 空的范围列落回空串，也就是全局——领域侧的 Scope 用空串表示根，与库里一致。
func toPolicy(rec database.RegistrationPolicyRecord) registration.Policy {
	return registration.Policy{
		Mode:               registration.Mode(rec.Mode),
		DefaultRoleID:      rec.DefaultRoleID,
		DefaultScope:       rbac.Scope(rec.DefaultScope),
		UpdatedBySubjectID: rec.UpdatedBySubjectID,
		UpdatedAt:          rec.UpdatedAt,
	}
}

// fromPolicy 把领域取值翻成记录。
func fromPolicy(policy registration.Policy) database.RegistrationPolicyRecord {
	return database.RegistrationPolicyRecord{
		Mode:               string(policy.Mode),
		DefaultRoleID:      policy.DefaultRoleID,
		DefaultScope:       string(policy.DefaultScope),
		UpdatedBySubjectID: policy.UpdatedBySubjectID,
		UpdatedAt:          policy.UpdatedAt,
	}
}

// toInvite 把记录翻成领域取值：两个可空时间列落回零值（不过期 / 未撤销）。
func toInvite(rec database.RegistrationInviteRecord) registration.Invite {
	invite := registration.Invite{
		ID:                 rec.ID,
		CodeHash:           rec.CodeHash,
		Label:              rec.Label,
		CreatedBySubjectID: rec.CreatedBySubjectID,
		CreatedAt:          rec.CreatedAt,
		MaxUses:            rec.MaxUses,
		UsedCount:          rec.UsedCount,
	}
	if rec.ExpiresAt != nil {
		invite.ExpiresAt = *rec.ExpiresAt
	}
	if rec.RevokedAt != nil {
		invite.RevokedAt = *rec.RevokedAt
	}
	return invite
}

// fromInvite 把领域取值翻成记录：零值时间落成 NULL，不落成年份 1。
//
// 后者在 MySQL 的 datetime 上根本插不进去（它的下界是 1000 年），而在 SQLite 上
// 会变成一串看起来像数据的 '0001-01-01'。
func fromInvite(invite registration.Invite) database.RegistrationInviteRecord {
	rec := database.RegistrationInviteRecord{
		ID:                 invite.ID,
		CodeHash:           invite.CodeHash,
		Label:              invite.Label,
		CreatedBySubjectID: invite.CreatedBySubjectID,
		CreatedAt:          invite.CreatedAt,
		MaxUses:            invite.MaxUses,
		UsedCount:          invite.UsedCount,
	}
	if !invite.ExpiresAt.IsZero() {
		expiresAt := invite.ExpiresAt
		rec.ExpiresAt = &expiresAt
	}
	if !invite.RevokedAt.IsZero() {
		revokedAt := invite.RevokedAt
		rec.RevokedAt = &revokedAt
	}
	return rec
}
