// Package gormstore 是 rbac.Store 与 rbac.MutableStore 的关系库实现。
//
// 它把"角色、主体、绑定关系"这三样事实落到库里，别的什么都不做：
// 判定在 internal/rbac，约束校验在 internal/rbac/constraints.go，
// 表结构在 internal/database/schema.go。本包只负责读写与两侧类型的转换。
package gormstore

import (
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 转换是记录与领域对象之间**唯一**的桥。
//
// 之所以不把数据库模型直接当领域对象用：一旦两者合并，"库里加一列"就
// 等于"判定多看到一个字段"，而那是一次不需要经过权限模型评审的改动。
// 分开之后，加一列必须在本文件里显式地"抬"到领域层才会被看见。
//
// 转换刻意保持 nil 与空切片的区别：nil 切片经 JSON 序列化后读回来仍是 nil，
// 而 `[]T{}` 读回来是空切片。两侧一致，契约测试才能用整体相等来断言，
// 不必为"哪种空"写特例。

func toRoleDefinition(rec database.RoleRecord) rbac.RoleDefinition {
	return rbac.RoleDefinition{
		ID:                    rec.ID,
		DisplayName:           rec.DisplayName,
		Builtin:               rec.Builtin,
		Permissions:           toPermissionCodes(rec.Permissions),
		Inherits:              rec.Inherits,
		MutuallyExclusiveWith: rec.MutuallyExclusiveWith,
	}
}

func fromRoleDefinition(role rbac.RoleDefinition) database.RoleRecord {
	return database.RoleRecord{
		ID:                    role.ID,
		DisplayName:           role.DisplayName,
		Builtin:               role.Builtin,
		Permissions:           toPermissionStrings(role.Permissions),
		Inherits:              role.Inherits,
		MutuallyExclusiveWith: role.MutuallyExclusiveWith,
	}
}

// 主体只有写入方向：当前没有"按标识读回主体"的需求——判定用的是凭证里
// 已确认的主体，存储侧只回答"这个主体登记过没有"。等真出现读回需求时
// 再补反向转换，而不是先写一个没人调用的函数放着。
func fromSubject(subject rbac.Subject) database.SubjectRecord {
	return database.SubjectRecord{
		ID:           subject.ID,
		Type:         string(subject.Type),
		DefaultScope: string(subject.DefaultScope),
	}
}

func toBindings(recs []database.RoleBindingRecord) []rbac.RoleBinding {
	out := make([]rbac.RoleBinding, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rbac.RoleBinding{
			SubjectID: rec.SubjectID,
			RoleID:    rec.RoleID,
			Scope:     rbac.Scope(rec.Scope),
		})
	}
	return out
}

func fromBinding(binding rbac.RoleBinding) database.RoleBindingRecord {
	return database.RoleBindingRecord{
		SubjectID: binding.SubjectID,
		RoleID:    binding.RoleID,
		Scope:     string(binding.Scope),
	}
}

func toPermissionCodes(codes []string) []rbac.PermissionCode {
	if codes == nil {
		return nil
	}
	out := make([]rbac.PermissionCode, 0, len(codes))
	for _, code := range codes {
		out = append(out, rbac.PermissionCode(code))
	}
	return out
}

func toPermissionStrings(codes []rbac.PermissionCode) []string {
	if codes == nil {
		return nil
	}
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		out = append(out, code.String())
	}
	return out
}
