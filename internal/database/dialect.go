package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/config"
)

// 后端取值。**这是合法取值的唯一来源**：配置模块的默认值是否仍在集合内，
// 由本包的守卫测试保证，而不是让两边各存一份字面量。
const (
	// DialectSQLite 是单文件、无独立服务的后端，默认选择。
	DialectSQLite Dialect = "sqlite"
	// DialectMySQL 是独立数据库服务的后端，用于多副本部署。
	DialectMySQL Dialect = "mysql"
)

// sqliteMemoryDSN 是 sqlite 的内存库连接串，用于测试与一次性场景。
//
// 单独列出来是因为它看起来像文件路径却不是，会被误当成路径去校验父目录。
const sqliteMemoryDSN = ":memory:"

// dialects 是已知后端的有序集合，驱动错误提示与守卫测试。
var dialects = []Dialect{DialectSQLite, DialectMySQL}

var (
	// ErrUnsupportedDialect 表示后端取值不在已知集合内。
	ErrUnsupportedDialect = errors.New("不支持的数据库后端")
	// ErrInvalidDSN 表示连接串不能用。
	ErrInvalidDSN = errors.New("数据库连接串不合法")
)

// Dialect 是数据库后端的类型。
type Dialect string

// sqlitePragmas 是补在"文件路径"形态连接串上的默认参数。
//
// 它们必须写在连接串里而不是开库后 Exec：busy_timeout 与 foreign_keys 是
// **每连接**生效的，用 Exec 只会设置到池子里的那一个连接上。
var sqlitePragmas = []string{
	// sqlite 只有一个写者。不设等待超时，一次并发写就会直接变成一个失败请求。
	"_pragma=busy_timeout(5000)",
	// 默认的日志模式会让读操作阻塞在写事务上。
	"_pragma=journal_mode(WAL)",
	// 本包不建外键。但底层默认关闭外键检查，会让**将来**加上的外键静默失效——
	// 静默失效的约束比没有约束更糟。
	"_pragma=foreign_keys(1)",
}

// poolSettings 是某后端下的连接池取值。
type poolSettings struct {
	maxOpenConns    int
	maxIdleConns    int
	connMaxLifetime time.Duration
	connMaxIdleTime time.Duration
}

// ParseDialect 解析后端取值，拒绝未登记的后端。
//
// 大小写不敏感，与配置模块对 log_level 的处理一致。
func ParseDialect(raw string) (Dialect, error) {
	d := Dialect(strings.ToLower(strings.TrimSpace(raw)))
	for _, known := range dialects {
		if d == known {
			return d, nil
		}
	}
	return "", fmt.Errorf("%w: %q，合法取值为 %s", ErrUnsupportedDialect, raw, knownDialects())
}

// NormalizeDSN 把配置里的连接串归一成驱动能直接使用的形态。
//
// 归一规则随后端而异，见 docs/design/persistence/schema.md。
func NormalizeDSN(d Dialect, dsn string) (string, error) {
	switch d {
	case DialectSQLite:
		return normalizeSQLiteDSN(dsn)
	case DialectMySQL:
		return normalizeMySQLDSN(dsn)
	default:
		return "", fmt.Errorf("%w: %q，合法取值为 %s", ErrUnsupportedDialect, d, knownDialects())
	}
}

// Dialector 构造该后端对应的 gorm 方言实现。
func (d Dialect) Dialector(dsn string) (gorm.Dialector, error) {
	normalized, err := NormalizeDSN(d, dsn)
	if err != nil {
		return nil, err
	}
	switch d {
	case DialectSQLite:
		return sqlite.Open(normalized), nil
	case DialectMySQL:
		return mysql.Open(normalized), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDialect, d)
	}
}

// Describe 返回连接串的**脱敏摘要**，供启动日志与错误信息使用。
//
// 连接串可能含口令，因此它不得原样进日志——这一项在服务端的地位等同于
// CLI 的凭证文件。摘要是"我改的那一行到底有没有被读到"的可答形式：
// 后端类型加上不含口令的定位信息。
//
// 取值无法解析时返回一句说明而不是回显内容：宁可少一条信息，
// 也不能把一个可能是口令的串写进日志。
func Describe(cfg config.DatabaseConfig) string {
	d, err := ParseDialect(cfg.Driver)
	if err != nil {
		return fmt.Sprintf("%s:（未登记的后端，连接串内容已省略）", cfg.Driver)
	}
	switch d {
	case DialectSQLite:
		return fmt.Sprintf("%s:%s", DialectSQLite, sqliteLocation(cfg.DSN))
	case DialectMySQL:
		parsed, err := mysqldriver.ParseDSN(strings.TrimSpace(cfg.DSN))
		if err != nil {
			return fmt.Sprintf("%s:（连接串无法解析，内容已省略）", DialectMySQL)
		}
		return fmt.Sprintf("%s:%s@%s/%s", DialectMySQL, parsed.User, parsed.Addr, parsed.DBName)
	default:
		return fmt.Sprintf("%s:（连接串内容已省略）", d)
	}
}

// applyPool 把该后端的连接池取值应用到连接上。
func (d Dialect) applyPool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	p := d.pool()
	sqlDB.SetMaxOpenConns(p.maxOpenConns)
	sqlDB.SetMaxIdleConns(p.maxIdleConns)
	sqlDB.SetConnMaxLifetime(p.connMaxLifetime)
	sqlDB.SetConnMaxIdleTime(p.connMaxIdleTime)
	return nil
}

// pool 返回该后端的连接池取值。
//
// sqlite 限制为单连接：它只有一个写者，把上限放到 1 以外并不会提高写入
// 并行度，只会让写冲突从"排队等待"变成"撞上 busy_timeout 之后报错"。
// 本仓库的数据量与访问频率都很低，读并发不值得用这个换取。
//
// MySQL 用常规取值：连接上限、闲置回收与生命周期上限各就位，
// 其中生命周期上限低于 MySQL 默认的 wait_timeout，避免拿到已被服务端
// 关闭的连接。
func (d Dialect) pool() poolSettings {
	switch d {
	case DialectSQLite:
		return poolSettings{maxOpenConns: 1, maxIdleConns: 1}
	case DialectMySQL:
		return poolSettings{
			maxOpenConns:    25,
			maxIdleConns:    5,
			connMaxLifetime: 30 * time.Minute,
			connMaxIdleTime: 5 * time.Minute,
		}
	default:
		return poolSettings{}
	}
}

// normalizeSQLiteDSN 归一 sqlite 的连接串。
//
// 取值有两种形态，按形状区分：**文件路径**（补上默认参数）与
// **完整连接串**（原样使用，自行控制底层参数）。
func normalizeSQLiteDSN(dsn string) (string, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", fmt.Errorf("%w: sqlite 的连接串不能为空", ErrInvalidDSN)
	}
	if sqliteIsCompleteDSN(dsn) {
		return dsn, nil
	}

	// 不自动创建目录：自动创建会让一个拼错的路径变成"在某个奇怪的地方
	// 建了个空库"，而现象是"数据全丢了"。宁可拒绝启动。
	dir := filepath.Dir(dsn)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("%w: sqlite 数据库文件所在目录 %s 不可用（%v）；"+
			"目录需已存在，服务端不代为创建", ErrInvalidDSN, dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: sqlite 数据库文件所在目录 %s 不是目录", ErrInvalidDSN, dir)
	}

	// 目标路径本身已存在且是目录时同样拒绝。这一条不查也不会让库开不起来，
	// 但错误会推迟到第一次查询，且来自驱动：`unable to open database file`
	// 不会提示"你把目录当成了文件"。配置错误应该在配置这一层被说清楚。
	if target, err := os.Stat(dsn); err == nil && target.IsDir() {
		return "", fmt.Errorf("%w: sqlite 连接串 %s 指向一个目录，期望是数据库文件路径", ErrInvalidDSN, dsn)
	}

	return "file:" + dsn + "?" + strings.Join(sqlitePragmas, "&"), nil
}

// normalizeMySQLDSN 归一 MySQL 的连接串：原样使用，只做一次解析校验。
//
// 不补默认参数：MySQL 的连接串已经有成熟且被广泛理解的形态，
// 在其上再叠一层默认值，只会让"为什么连到了别处"更难回答。
func normalizeMySQLDSN(dsn string) (string, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", fmt.Errorf("%w: MySQL 的连接串不能为空", ErrInvalidDSN)
	}
	// 解析失败的错误来自驱动，可能回显连接串里的某个片段。
	// 口令位于 user:pass@ 段，驱动不会引用它；这一点由测试守着。
	if _, err := mysqldriver.ParseDSN(dsn); err != nil {
		return "", fmt.Errorf("%w: MySQL 连接串无法解析（%w）", ErrInvalidDSN, err)
	}
	return dsn, nil
}

// sqliteIsCompleteDSN 判断取值是否已经是完整连接串，而不是文件路径。
func sqliteIsCompleteDSN(dsn string) bool {
	return dsn == sqliteMemoryDSN ||
		strings.HasPrefix(dsn, "file:") ||
		strings.Contains(dsn, "?")
}

// sqliteLocation 从 sqlite 连接串里取出用于日志的定位信息（文件路径）。
func sqliteLocation(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == sqliteMemoryDSN {
		return sqliteMemoryDSN
	}
	if !strings.HasPrefix(dsn, "file:") {
		// 路径形态的连接串里不会出现 "?"，取值本身就是定位信息。
		if i := strings.Index(dsn, "?"); i >= 0 {
			return dsn[:i]
		}
		return dsn
	}
	rest := strings.TrimPrefix(dsn, "file:")
	if i := strings.Index(rest, "?"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// knownDialects 把已知后端拼成可读的列举，用于错误提示。
func knownDialects() string {
	parts := make([]string, 0, len(dialects))
	for _, d := range dialects {
		parts = append(parts, string(d))
	}
	return strings.Join(parts, " / ")
}
