package migrate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 迁移后的库结构快照。
//
// **这是"迁移冻结"的守门人。** 迁移体用的是 AutoMigrate，因此改了
// internal/database/schema.go 里的模型却忘了追加迁移时，新库会悄悄长出
// 旧库没有的列——而这个差异只有等到某个已上线的库报"这一列怎么没有"
// 才会暴露。快照把"当前代码定义出来的结构"钉死在这里：模型变了它就失败，
// 失败的人必须显式回答"这是我想要的吗"，是则追加迁移并更新这里的期望。
//
// 期望值按 **sqlite** 记录。换后端时这里会整体变化——那时应当再留一份
// 该后端的快照，而不是把这份改成两边都不像。
const wantSchema = `table client_events
  action text pk=false null=true
  attrs text pk=false null=true
  client text pk=false null=true
  client_trace_id text pk=false null=true
  duration_ms integer pk=false null=true
  id integer pk=true null=true
  occurred_at datetime pk=false null=true
  result text pk=false null=true
  subject_id text pk=false null=true
  surface text pk=false null=true
table galaxy_asset_tags
  asset_id text pk=true null=true
  project_id text pk=true null=true
  tag text pk=true null=true
table galaxy_assets
  digest text pk=false null=true
  filename text pk=false null=true
  id text pk=true null=true
  media_kind text pk=false null=true
  media_type text pk=false null=true
  notes text pk=false null=true
  project_id text pk=false null=true
  size_bytes integer pk=false null=true
  title text pk=false null=true
  uploaded_at datetime pk=false null=true
table galaxy_drafts
  manifest text pk=false null=true
  project_id text pk=true null=true
  slot text pk=true null=true
  updated_at datetime pk=false null=true
table galaxy_preview_grants
  created_at datetime pk=false null=true
  expires_at datetime pk=false null=true
  project_id text pk=false null=true
  slot text pk=false null=true
  subject_id text pk=false null=true
  token text pk=true null=true
table galaxy_project_slots
  current_publication_id text pk=false null=true
  project_id text pk=true null=true
  slot text pk=true null=true
table galaxy_projects
  created_at datetime pk=false null=true
  description text pk=false null=true
  id text pk=true null=true
  name text pk=false null=true
  owner_subject_id text pk=false null=true
  updated_at datetime pk=false null=true
table galaxy_publications
  id text pk=true null=true
  manifest text pk=false null=true
  project_id text pk=false null=true
  published_at datetime pk=false null=true
  published_by_subject_id text pk=false null=true
  slot text pk=false null=true
  version_id text pk=false null=true
table galaxy_versions
  id text pk=true null=true
  manifest text pk=false null=true
  project_id text pk=false null=true
  render_rules_version integer pk=false null=true
  saved_at datetime pk=false null=true
  seq integer pk=false null=true
  slot text pk=false null=true
table identities
  display text pk=false null=true
  external_id text pk=true null=true
  source text pk=true null=true
  subject_id text pk=false null=true
table role_bindings
  role_id text pk=true null=true
  scope text pk=true null=true
  subject_id text pk=true null=true
table roles
  builtin numeric pk=false null=true
  display_name text pk=false null=true
  id text pk=true null=true
  inherits text pk=false null=true
  mutually_exclusive_with text pk=false null=true
  permissions text pk=false null=true
table schema_migrations
  id text pk=true null=true
table scopes
  display_name text pk=false null=true
  path text pk=true null=true
table sessions
  default_scope text pk=false null=true
  expires_at datetime pk=false null=true
  issued_at datetime pk=false null=true
  subject_id text pk=false null=true
  subject_type text pk=false null=true
  token_hash text pk=true null=true
table skill_favorites
  created_at datetime pk=false null=true
  skill_id text pk=true null=true
  subject_id text pk=true null=true
table skill_tags
  skill_id text pk=true null=true
  tag text pk=true null=true
table skill_usage_daily
  day text pk=true null=true
  skill_id text pk=true null=true
  subject_id text pk=true null=true
  used_at datetime pk=false null=true
table skill_versions
  commit text pk=false null=true
  created_at datetime pk=false null=true
  description text pk=false null=true
  files text pk=false null=true
  id text pk=true null=true
  name text pk=false null=true
  skill_id text pk=false null=true
  skipped_files integer pk=false null=true
table skills
  cover_key text pk=false null=true
  created_at datetime pk=false null=true
  current_version_id text pk=false null=true
  id text pk=true null=true
  source_commit text pk=false null=true
  source_name text pk=false null=true
  source_owner text pk=false null=true
  source_ref text pk=false null=true
  source_sub_path text pk=false null=true
  summary text pk=false null=true
  title text pk=false null=true
  updated_at datetime pk=false null=true
table subject_profiles
  avatar_key text pk=false null=true
  bio text pk=false null=true
  nickname text pk=false null=true
  subject_id text pk=true null=true
  updated_at datetime pk=false null=true
table subjects
  default_scope text pk=false null=true
  id text pk=true null=true
  type text pk=false null=true
`

func TestSchemaSnapshot(t *testing.T) {
	db := openTestDB(t)
	if err := Run(context.Background(), db, zap.NewNop()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	got := schemaDump(t, db)
	if strings.TrimRight(got, "\n") != strings.TrimRight(wantSchema, "\n") {
		t.Errorf("库结构与冻结的快照不一致。\n"+
			"若这是预期的结构变更：追加一条迁移，并把本测试的 wantSchema 更新为新快照。\n"+
			"若这不是：说明你改了 internal/database/schema.go 却没意识到。\n"+
			"--- 实际 ---\n%s\n--- 期望 ---\n%s", got, wantSchema)
	}
}

// schemaDump 把库结构渲染成稳定的文本：表按名排序，列按名排序。
//
// 顺序必须由这里定死，不能依赖驱动返回的次序——那份次序属于实现细节，
// 快照要冻结的是结构本身，不是驱动的遍历顺序。
func schemaDump(t *testing.T, db *gorm.DB) string {
	t.Helper()

	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatalf("读取表清单失败: %v", err)
	}
	sort.Strings(tables)

	var b strings.Builder
	for _, table := range tables {
		// 跳过引擎自己的记账表（sqlite_sequence 等）。它们不是"库结构"的一部分，
		// 而是 sqlite 为自增列维护的内部表——把它冻进快照，会让"某张表用了自增
		// 主键"这件事看起来像一次结构变更，也会让快照随引擎版本漂移。
		if strings.HasPrefix(table, "sqlite_") {
			continue
		}
		columns, err := db.Migrator().ColumnTypes(table)
		if err != nil {
			t.Fatalf("读取表 %s 的列失败: %v", table, err)
		}
		lines := make([]string, 0, len(columns))
		for _, column := range columns {
			primary := columnFlag(t, table, column.Name(), "主键标记", column.PrimaryKey)
			nullable := columnFlag(t, table, column.Name(), "可空标记", column.Nullable)
			lines = append(lines, fmt.Sprintf("%s %s pk=%t null=%t",
				column.Name(), column.DatabaseTypeName(), primary, nullable))
		}
		sort.Strings(lines)

		fmt.Fprintf(&b, "table %s\n", table)
		for _, line := range lines {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	return b.String()
}

// columnFlag 解开 gorm 的"值 + 是否已知"二元返回。
//
// 驱动答不上来时不能默默记成 false：快照里一个假的 false 比缺一行更糟——
// 它会让"结构变了"看起来像"结构没变"。
func columnFlag(t *testing.T, table, column, what string, read func() (bool, bool)) bool {
	t.Helper()
	value, known := read()
	if !known {
		t.Fatalf("驱动没有给出 %s.%s 的%s", table, column, what)
	}
	return value
}
