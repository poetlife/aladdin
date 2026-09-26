package gormstore

import (
	"fmt"

	"github.com/poetlife/aladdin/internal/identity"
)

// 本文件是会话存储实现与上层之间**唯一的错误契约面**。
//
// 它要守住两组区分，混掉任何一组都会让排障指向错误的方向：
//
//   - "会话不存在"与"库用不了"。前者是正常的否定结论（这份凭证不成立），
//     后者必须让请求快速失败。混为一谈会把一次数据库故障表现成
//     "所有人的登录都失效了"，运维于是去查认证配置。
//   - 而所谓"不存在"里，**不区分**不存在 / 已撤销 / 已过期。这三者
//     对调用方是同一件事，分开会让探测者能从响应里读出"这个字符串
//     曾经有效"（见 docs/design/identity/session-token.md）。

// unavailable 把底层错误包装成存储不可用，并说明当时在做什么。
//
// 动作描述里只有动作，**没有凭证摘要**：摘要不是凭证，但它也没有排障价值
// ——需要定位的是"哪一步、什么错"，而摘要能定位到的是"哪一行"，
// 那是库自己的事。
func unavailable(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", identity.ErrStoreUnavailable, action, err)
}
