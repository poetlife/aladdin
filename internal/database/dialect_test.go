package database

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/config"
)

// 后端取值的合法集合只有本包一份。配置模块的默认值必须落在集合内——
// 它写的是字面量 "sqlite"，而判据在这里。两边漂移的表现是**服务起不来**，
// 且错误信息指向配置而不是指向那个已经过时的默认值。
func TestConfigDefaultsPointAtKnownDialect(t *testing.T) {
	for _, raw := range []string{
		config.DefaultDatabaseDriver,
		config.DefaultServer().Database.Driver,
	} {
		if _, err := ParseDialect(raw); err != nil {
			t.Errorf("配置默认后端 %q 不在合法集合内: %v", raw, err)
		}
	}
}

func TestParseDialect(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    Dialect
		wantErr error
	}{
		{"sqlite", "sqlite", DialectSQLite, nil},
		{"mysql", "mysql", DialectMySQL, nil},
		{"大小写不敏感", "MySQL", DialectMySQL, nil},
		{"两端空白被忽略", "  sqlite\t", DialectSQLite, nil},
		{"未登记的后端", "postgres", "", ErrUnsupportedDialect},
		{"空串", "", "", ErrUnsupportedDialect},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDialect(tc.raw)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v，期望 %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("ParseDialect(%q) = %q，期望 %q", tc.raw, got, tc.want)
			}
		})
	}
}

// 错误信息要能自答"那到底能填什么"，否则用户只能去翻代码。
func TestParseDialectErrorListsKnownValues(t *testing.T) {
	_, err := ParseDialect("postgres")
	if err == nil {
		t.Fatal("期望报错")
	}
	for _, d := range dialects {
		if !strings.Contains(err.Error(), string(d)) {
			t.Errorf("错误信息 %q 没有列出后端 %q", err, d)
		}
	}
}

func TestNormalizeSQLiteDSN(t *testing.T) {
	t.Run("文件路径补上默认参数", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "aladdin.db")

		got, err := NormalizeDSN(DialectSQLite, path)
		if err != nil {
			t.Fatalf("归一失败: %v", err)
		}
		if !strings.HasPrefix(got, "file:"+path+"?") {
			t.Fatalf("归一结果 %q 没有指向原路径", got)
		}
		// 三个默认参数一个都不能少：少一个的表现是"偶发写入失败"或
		// "读被写事务阻塞"，两者都极难从现象反推回连接串。
		for _, pragma := range sqlitePragmas {
			if !strings.Contains(got, pragma) {
				t.Errorf("归一结果 %q 缺少默认参数 %q", got, pragma)
			}
		}
	})

	t.Run("内存库原样", func(t *testing.T) {
		got, err := NormalizeDSN(DialectSQLite, sqliteMemoryDSN)
		if err != nil {
			t.Fatalf("归一失败: %v", err)
		}
		if got != sqliteMemoryDSN {
			t.Errorf("got %q，期望原样返回 %q", got, sqliteMemoryDSN)
		}
	})

	t.Run("已是完整连接串则原样", func(t *testing.T) {
		const dsn = "file:/tmp/x.db?_pragma=busy_timeout(1000)"
		got, err := NormalizeDSN(DialectSQLite, dsn)
		if err != nil {
			t.Fatalf("归一失败: %v", err)
		}
		if got != dsn {
			t.Errorf("got %q，期望原样返回——调用方显式写的参数不该被覆盖", got)
		}
	})

	t.Run("目录不存在时拒绝", func(t *testing.T) {
		// 不自动建目录：那会让一个拼错的路径变成"在某个奇怪的地方建了个
		// 空库"，而现象是"数据全丢了"。宁可拒绝启动。
		missing := filepath.Join(t.TempDir(), "not-there", "aladdin.db")
		if _, err := NormalizeDSN(DialectSQLite, missing); !errors.Is(err, ErrInvalidDSN) {
			t.Errorf("err = %v，期望 ErrInvalidDSN", err)
		}
	})

	t.Run("路径指向目录时拒绝", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := NormalizeDSN(DialectSQLite, dir); !errors.Is(err, ErrInvalidDSN) {
			t.Errorf("err = %v，期望 ErrInvalidDSN", err)
		}
	})

	t.Run("空串拒绝", func(t *testing.T) {
		if _, err := NormalizeDSN(DialectSQLite, "   "); !errors.Is(err, ErrInvalidDSN) {
			t.Errorf("err = %v，期望 ErrInvalidDSN", err)
		}
	})
}

func TestNormalizeMySQLDSN(t *testing.T) {
	t.Run("合法连接串原样", func(t *testing.T) {
		const dsn = "aladdin:secret@tcp(127.0.0.1:3306)/aladdin?parseTime=true"
		got, err := NormalizeDSN(DialectMySQL, dsn)
		if err != nil {
			t.Fatalf("归一失败: %v", err)
		}
		if got != dsn {
			t.Errorf("got %q，期望原样返回——不往成熟形态上再叠一层默认值", got)
		}
	})

	t.Run("无法解析时拒绝", func(t *testing.T) {
		if _, err := NormalizeDSN(DialectMySQL, "这不是连接串"); !errors.Is(err, ErrInvalidDSN) {
			t.Errorf("err = %v，期望 ErrInvalidDSN", err)
		}
	})

	t.Run("空串拒绝", func(t *testing.T) {
		if _, err := NormalizeDSN(DialectMySQL, ""); !errors.Is(err, ErrInvalidDSN) {
			t.Errorf("err = %v，期望 ErrInvalidDSN", err)
		}
	})

	// 解析失败的错误会被拼进"服务端起不来"的提示里，因此它同样不能回显取值。
	t.Run("无法解析时不把口令带进错误信息", func(t *testing.T) {
		// 网络地址缺右括号：驱动会在解析阶段就失败。
		_, err := NormalizeDSN(DialectMySQL, "aladdin:"+testPassword+"@tcp(127.0.0.1:3306")
		if err == nil {
			t.Fatal("期望报错")
		}
		if strings.Contains(err.Error(), testPassword) {
			t.Fatalf("错误信息 %q 里出现了口令", err)
		}
	})
}

// testPassword 是贯穿本文件各条泄漏断言的取值。
//
// 放在包级而不是某个用例里：口令泄漏有两条路径（日志摘要与错误信息），
// 两条都要用同一个取值来断言，各写一个常量就有一边可能被漏掉。
const testPassword = "s3cret-p@ssw0rd"

// 连接串可能含口令，而它会被写进启动日志与错误信息。
// 这是本包唯一一条"泄漏即事故"的红线，因此单独守着。
func TestDescribeNeverLeaksCredentials(t *testing.T) {
	const password = testPassword

	t.Run("mysql 只给出口令之外的定位信息", func(t *testing.T) {
		got := Describe(config.DatabaseConfig{
			Driver: "mysql",
			DSN:    "aladdin:" + password + "@tcp(127.0.0.1:3306)/aladdin",
		})
		if strings.Contains(got, password) {
			t.Fatalf("摘要 %q 里出现了口令", got)
		}
		for _, want := range []string{"mysql", "aladdin", "127.0.0.1:3306"} {
			if !strings.Contains(got, want) {
				t.Errorf("摘要 %q 缺少定位信息 %q", got, want)
			}
		}
	})

	t.Run("sqlite 给出文件路径", func(t *testing.T) {
		got := Describe(config.DatabaseConfig{Driver: "sqlite", DSN: "data/aladdin.db"})
		if got != "sqlite:data/aladdin.db" {
			t.Errorf("got %q，期望带路径的摘要", got)
		}
	})

	t.Run("完整的 sqlite 连接串只取路径", func(t *testing.T) {
		got := Describe(config.DatabaseConfig{
			Driver: "sqlite",
			DSN:    "file:/tmp/aladdin.db?_pragma=busy_timeout(5000)",
		})
		if got != "sqlite:/tmp/aladdin.db" {
			t.Errorf("got %q，期望只保留路径部分", got)
		}
	})

	t.Run("取值无法解析时省略内容", func(t *testing.T) {
		// 宁可少一条信息，也不能把一个可能是口令的串写进日志。
		got := Describe(config.DatabaseConfig{Driver: "mysql", DSN: password})
		if strings.Contains(got, password) {
			t.Fatalf("摘要 %q 里出现了原始内容", got)
		}
	})

	t.Run("未登记的后端不回显连接串", func(t *testing.T) {
		got := Describe(config.DatabaseConfig{Driver: "postgres", DSN: password})
		if strings.Contains(got, password) {
			t.Fatalf("摘要 %q 里出现了原始内容", got)
		}
		if !strings.Contains(got, "postgres") {
			t.Errorf("摘要 %q 应保留用户填的后端名，它是唯一的线索", got)
		}
	})
}

// sqlite 只有一个写者：连接池放到 1 以外不会提高写入并行度，
// 只会让写冲突从"排队等待"变成"撞上 busy_timeout 之后报错"。
func TestSQLitePoolIsSingleConnection(t *testing.T) {
	pool := DialectSQLite.pool()
	if pool.maxOpenConns != 1 || pool.maxIdleConns != 1 {
		t.Errorf("sqlite 连接池 = %+v，期望单连接", pool)
	}
}
