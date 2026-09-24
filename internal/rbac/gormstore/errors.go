package gormstore

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件是存储实现与上层之间**唯一的错误契约面**。
//
// 它必须守住"没有这条数据"与"库用不了"的区分。两者在数据面上都是
// "查不到"，但结论完全相反：前者是正常答案（该主体确实没被授予这个角色），
// 后者必须让请求快速失败。混为一谈的后果是把一次数据库故障表现成一次
// 权限问题——运维看到的是"用户突然没权限了"，于是去改权限配置。
//
// 映射到领域错误之后，internal/server 的 toConnectError 就能把
// ErrStoreUnavailable 翻成 CodeUnavailable，把"找不到"翻成 CodeNotFound。

// unavailable 把底层错误包装成存储不可用，并说明当时在做什么。
//
// 动作描述是给排障用的：同为"库用不了"，读角色列表与写绑定指向的
// 排查方向不同。它不含连接信息，也不含任何凭证。
func unavailable(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", rbac.ErrStoreUnavailable, action, err)
}

// roleLookupError 翻译"按标识取角色"时可能出现的两种失败。
func roleLookupError(err error, roleID string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: %s", rbac.ErrRoleNotFound, roleID)
	}
	return unavailable("读取角色 "+roleID, err)
}

// subjectLookupError 翻译"判断主体是否登记过"时可能出现的两种失败。
//
// 主体本身不出现在接口的返回值里，因此这里不走 gorm.ErrRecordNotFound：
// 调用方用 Count 判断存在性，"不存在"是 count == 0，不是错误。
func subjectLookupError(err error, subjectID string) error {
	return unavailable("读取主体 "+subjectID, err)
}
