// Package entity 是**表结构的唯一信源**：库里的每一张表、每一列都在这里定义。
// 少数列里的 JSON 元素（如 SkillFileRecord）也在这里，因为它们同样是"库里的形状"。
//
// 它只回答"库里长什么样"：记录类型、列上的 gorm tag、表名。连接与方言在
// internal/database，建表与版本演进在 internal/database/migrate，查询与领域类型
// 转换在各领域的 gormstore。三件事分开，改表结构时只需要动本包 + 一条迁移。
//
// 记录类型与领域对象是分开的：领域对象（rbac.RoleDefinition 等）表达"权限模型里
// 有什么"，记录类型表达"它在库里长什么样"。两者由 internal/<domain>/gormstore
// 转换，转换是唯一一处——记录里加一列不会悄悄改变判定看到的东西。
//
// 本包不 import 任何领域包，也不建立连接：它是被 gormstore 与 migrate 依赖的
// 叶子。表结构是否与迁移一致，由 internal/database/migrate 的结构快照测试守着。
package entity
