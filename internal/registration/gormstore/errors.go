// Package gormstore 是 registration.Store 的关系库实现。
//
// 它持有的是连接而不是配置：方言解析、连接串归一、连接池取值都在
// internal/database 完成，迁移由 internal/database/migrate 推进，本包只消费一个
// 已经准备好表的 *gorm.DB。
package gormstore

import (
	"fmt"

	"github.com/poetlife/aladdin/internal/registration"
)

// 本文件是注册存储实现与上层之间**唯一的错误契约面**。
//
// 它要守住两组区分，混掉任何一组都会让排障指向错误的方向：
//
//   - "兑换不动"与"库用不了"。前者是正常的否定结论（这份码不成立），后者必须
//     让请求快速失败。混为一谈会把一次数据库故障表现成"所有邀请码都失效了"，
//     运维于是去重发码。
//   - 而所谓"兑换不动"里，**不区分**不存在 / 已过期 / 次数用尽 / 已撤销。这四者
//     对调用方是同一件事，分开会让探测者能从响应里读出"这个字符串曾经有效"
//     （见 docs/design/identity/registration.md）。

// unavailable 把底层错误包装成存储不可用，并说明当时在做什么。
//
// 动作描述里只有动作，**没有码摘要**：摘要不是明文，但它也没有排障价值——需要
// 定位的是"哪一步、什么错"，而摘要能定位到的是"哪一行"，那是库自己的事。
func unavailable(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", registration.ErrStoreUnavailable, action, err)
}
