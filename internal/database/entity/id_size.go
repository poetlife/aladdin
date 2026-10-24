package entity

// IDSize 是所有标识类列的长度上限。
//
// 取 191 而不是更大的值：utf8mb4 下它对应 764 字节，三列复合主键
// （见 RoleBindingRecord）合计仍在 InnoDB 的索引长度限制之内。
// 本仓库的标识都是 `system.admin`、`tenant/acme` 这种形状，
// 这个上限远超实际需要。
//
// 导出是因为它不止约束这三张业务表：迁移版本表的标识列同样要落在
// 同一个上限内（见 internal/database/migrate）。取值只有一个来源。
const IDSize = 191
