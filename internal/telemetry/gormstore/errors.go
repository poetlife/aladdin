package gormstore

import (
	"fmt"

	"github.com/poetlife/aladdin/internal/telemetry"
)

// 本文件是事件存储实现与上层之间**唯一的错误契约面**。
//
// 它要守住一组区分：「窗口内没有事件」与「库用不了」。前者是正常的空答案
// （最近确实没人做这些动作），后者必须让请求快速失败。混为一谈会把一次数据库
// 故障表现成"最近没人用"，排障的人于是去看一个根本没坏的东西。
//
// 与 RBAC、认证那些存储不同，这里**没有"找不到"这一类**：本模块的读取都是
// 集合查询，空集合是合法结果而不是错误。因此这个文件只有一个包装函数。

// unavailable 把底层错误包装成存储不可用，并说明当时在做什么。
//
// 动作描述是给排障用的：同为"库用不了"，写事件与读计数指向的排查方向不同。
// 它不含连接信息，也不含任何凭证——事件里本就没有凭证。
func unavailable(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", telemetry.ErrStoreUnavailable, action, err)
}
