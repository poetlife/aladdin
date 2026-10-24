package entity

import "time"

// ClientEventRecord 是一条**客户端事件**在库里的一行（见
// docs/observability.md 的「客户端事件」）。
//
// 它是写侧落库的读模型，只服务于站内的只读管理页。存**原始事件**而不是按分钟
// 汇总，取的是"一张表同时回答计数与明细"：客户端事件是低频批量上报（只有出站前
// 被拦下的那一档），30 天的行数可控，没有双写与 rollup 一致性的成本。量级真涨到
// 需要汇总表时再追加一条迁移——那正是版本化迁移存在的意义。
//
// 与其它表不同，这一张**只增不减，靠保留期回收**：行本身没有"被撤销"的语义，
// 超出窗口的行由启动时的回收删掉（见 internal/telemetry 的保留期常量）。
//
// 与主体表之间**不建外键**，理由与绑定表、会话表相同：主体是否存在是领域约束，
// 唯一入口是认证流程的登记动作，不复制成库约束。匿名事件根本没有主体，加外键
// 还会把"合法的空值"变成一个结构错误。
type ClientEventRecord struct {
	// ID 是自增主键。
	//
	// 事件**没有自然主键**：同一毫秒内的两条相同事件是两条合法记录，用
	// (时间, 动作) 之类的组合当键会把它们合并成一条。自增列因此不是图省事，而是
	// 承认"每一条都是独立事实"。
	ID uint64 `gorm:"primaryKey;autoIncrement"`
	// OccurredAt 是服务端收到并落库的时刻——**不由上报端提供**。
	//
	// 建索引是因为两条常规路径都按它筛选：时间窗内的计数与明细，以及保留期回收。
	// 没有它，这两者都会退化成对一张只增不减的表做全表扫描。
	OccurredAt time.Time `gorm:"index"`
	// Client 是上报端（`web` / `cli`）。取值经服务端白名单折算，不是上报端的原值。
	Client string
	// Surface 是事件发生在哪个界面/入口。
	Surface string
	// Action 是动作名，取值来自服务端的动作允许清单。**它不是枚举列**：动作清单随
	// 业务扩展，而这里是历史数据——用枚举会让"昨天记下的动作"在清单变更后无法表示。
	Action string
	// Result 是结局（`ok` / `fail` / `cancel` / `blocked`）。
	Result string
	// DurationMS 是上报端声明的耗时；0 表示未提供。
	DurationMS uint32
	// ClientTraceID 是上报端当时所在链路的 trace_id；空表示未提供或不合法。
	//
	// 注意它**不是**本行所属请求的 trace_id，而是上报端当时那条链路的，因此读侧
	// 与日志里的字段名一致，都叫 client_trace_id。
	ClientTraceID string `gorm:"size:64"`
	// SubjectID 是事件所属主体；空表示匿名。由服务端从会话得出，不由请求提供。
	SubjectID string `gorm:"size:191"`
	// Attrs 是白名单属性，与主体权限集合同样**整份读写**——读侧要么整行取出展示，
	// 要么按 (client, action, result) 计数，没有任何"按某个属性值反查"的查询。
	// 拆成关联表会引入一次 join 与一套写入顺序，换不到当前需要的能力。
	Attrs map[string]string `gorm:"serializer:json;type:text"`
}

// TableName 实现 gorm 的表名解析。
func (ClientEventRecord) TableName() string { return "client_events" }
