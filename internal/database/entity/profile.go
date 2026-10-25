package entity

import "time"

// SubjectProfileRecord 是一个主体的展示信息在库里的一行。
//
// 它是一张**独立于主体表**的表，而不是主体表上多出来的几列。两个理由：
//
//   - 主体表是判定路径要读的数据（"这个主体登记了没有"），而档案是纯展示
//     数据，任何一次判定都不需要它的一个字节；
//   - 主体表是**整行覆盖**写入的（登录登记、引导、开发种子都走同一条路径），
//     昵称若落在那一行上，任何一次主体写入都会把它顺手擦成空值。
//
// 行是**惰性创建**的：主体登记时不写这一行，本人第一次保存档案才写入。
// 认证流程因此完全不依赖本表，依赖方向单向（见
// docs/design/profile/README.md）。
//
// 与主体表之间**不建外键**，理由与绑定表、会话表相同：主体是否存在是领域
// 约束，唯一入口是认证流程的登记动作，不复制成库约束。
type SubjectProfileRecord struct {
	// SubjectID 是主体标识，主键。一个主体最多一条档案。
	SubjectID string `gorm:"primaryKey;size:191"`
	// Nickname 是主体自己设置的昵称。空表示未设置，展示时回退。
	//
	// **刻意不加唯一索引。** 唯一约束会把它变成一个查找键，也就是第二条身份
	// 路径；一旦有了唯一性，"按昵称查人"的需求就会跟着出现——而那正是邮箱
	// 这条路已经踩过的坑。本表也因此不建按昵称的索引：没有任何一条读取路径
	// 按昵称查。
	Nickname string
	// Bio 是简介。空表示未填写。
	Bio string
	// AvatarKey 是头像在对象存储里的对象键。空表示没有头像。
	//
	// **这里存的是键，不是字节。** 字节在对象存储（见
	// docs/design/profile/avatar-storage.md）；本列是"这个主体有没有头像"的
	// 权威，对象存储上可能留下无从被引用的孤儿对象。
	AvatarKey string
	// UpdatedAt 是最后一次变更的时间。只用于展示与排障，不参与判定。
	UpdatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (SubjectProfileRecord) TableName() string { return "subject_profiles" }
