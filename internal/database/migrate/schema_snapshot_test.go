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
// **这是"迁移冻结"的守门人之一。** 迁移体用的是 AutoMigrate，因此改
// internal/database/entity 里的模型时，当前代码定义出来的结构会先在这里失败，
// 逼你回答"这次变更该不该有配套迁移"。但它只证明**全新库**跑完所有迁移后的
// 形状；旧迁移体引用的是当前模型，所以"已发布迁移不可修改"最终仍靠纪律守住
// ——不能因为更新了快照、新库跑绿了，就跳过给旧库的那条迁移。
//
// **索引也在冻结范围内，而且不只是为了性能。** 唯一索引是语义约束：
// 例如邀请码的 code_hash 唯一索引是"兑换只按摘要进"这条行为的地基，
// 它丢了不会让任何业务用例变红，只会让一次重复写入变成两条可兑换的行。
// 因此这里把显式索引连同 unique 标记一起冻住；引擎自动生成的
// sqlite_autoindex_* 与主键索引不在此列——它们是主键的实现细节，
// 已经由列的 pk 标记表达。
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
  index idx_client_events_occurred_at unique=false columns=occurred_at
table galaxy_asset_tags
  asset_id text pk=true null=true
  project_id text pk=true null=true
  tag text pk=true null=true
  index idx_galaxy_asset_tags_tag unique=false columns=project_id,tag
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
  index idx_galaxy_assets_project_id unique=false columns=project_id
table galaxy_attachments
  description text pk=false null=true
  digest text pk=false null=true
  filename text pk=false null=true
  id text pk=true null=true
  project_id text pk=false null=true
  size_bytes integer pk=false null=true
  uploaded_at datetime pk=false null=true
  uploaded_by_subject_id text pk=false null=true
  version_id text pk=false null=true
  index idx_galaxy_attachments_project_id unique=false columns=project_id
  index idx_galaxy_attachments_version_id unique=false columns=version_id
table galaxy_draft_snapshots
  created_at datetime pk=false null=true
  id text pk=true null=true
  manifest text pk=false null=true
  project_id text pk=false null=true
  replaced_by_subject_id text pk=false null=true
  seq integer pk=false null=true
  slot text pk=false null=true
  source text pk=false null=true
  index idx_galaxy_draft_snapshots_slot unique=false columns=project_id,slot,seq
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
  index idx_galaxy_preview_grants_project_id unique=false columns=project_id
table galaxy_project_slots
  current_publication_id text pk=false null=true
  project_id text pk=true null=true
  slot text pk=true null=true
table galaxy_projects
  blind_watermark numeric pk=false null=true
  created_at datetime pk=false null=true
  description text pk=false null=true
  id text pk=true null=true
  name text pk=false null=true
  owner_subject_id text pk=false null=true
  updated_at datetime pk=false null=true
  index idx_galaxy_projects_owner_subject_id unique=false columns=owner_subject_id
table galaxy_publications
  id text pk=true null=true
  manifest text pk=false null=true
  project_id text pk=false null=true
  published_at datetime pk=false null=true
  published_by_subject_id text pk=false null=true
  slot text pk=false null=true
  version_id text pk=false null=true
table galaxy_versions
  description text pk=false null=true
  id text pk=true null=true
  manifest text pk=false null=true
  project_id text pk=false null=true
  render_rules_version integer pk=false null=true
  saved_at datetime pk=false null=true
  seq integer pk=false null=true
  slot text pk=false null=true
  index idx_galaxy_versions_project_id unique=false columns=project_id
  index idx_galaxy_versions_slot unique=false columns=slot
table galaxy_watermark_visits
  code text pk=true null=true
  identity_key text pk=false null=true
  project_id text pk=false null=true
  slot text pk=false null=true
  source_address text pk=false null=true
  subject_id text pk=false null=true
  user_agent_digest text pk=false null=true
  visited_at datetime pk=false null=true
  index idx_galaxy_watermark_reuse unique=false columns=identity_key,visited_at
  index idx_galaxy_watermark_visits_project_id unique=false columns=project_id
table identities
  display text pk=false null=true
  external_id text pk=true null=true
  source text pk=true null=true
  subject_id text pk=false null=true
  index idx_identities_subject_id unique=false columns=subject_id
table registration_invites
  code_hash text pk=false null=true
  created_at datetime pk=false null=true
  created_by_subject_id text pk=false null=true
  expires_at datetime pk=false null=true
  id text pk=true null=true
  label text pk=false null=true
  max_uses integer pk=false null=true
  revoked_at datetime pk=false null=true
  used_count integer pk=false null=true
  index idx_registration_invites_code_hash unique=true columns=code_hash
table registration_policy
  default_role_id text pk=false null=true
  default_scope text pk=false null=true
  id text pk=true null=true
  mode text pk=false null=true
  updated_at datetime pk=false null=true
  updated_by_subject_id text pk=false null=true
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
  index idx_sessions_expires_at unique=false columns=expires_at
table skill_favorites
  created_at datetime pk=false null=true
  skill_id text pk=true null=true
  subject_id text pk=true null=true
  index idx_skill_favorites_subject_id unique=false columns=subject_id
table skill_images
  created_at datetime pk=false null=true
  id text pk=true null=true
  object_key text pk=false null=true
  position integer pk=false null=true
  size_bytes integer pk=false null=true
  skill_id text pk=false null=true
  index idx_skill_images_order unique=false columns=skill_id,position
table skill_tags
  skill_id text pk=true null=true
  tag text pk=true null=true
  index idx_skill_tags_tag unique=false columns=tag,skill_id
table skill_usage_daily
  day text pk=true null=true
  skill_id text pk=true null=true
  subject_id text pk=true null=true
  used_at datetime pk=false null=true
  index idx_skill_usage_daily_day unique=false columns=day
table skill_versions
  commit text pk=false null=true
  created_at datetime pk=false null=true
  description text pk=false null=true
  files text pk=false null=true
  id text pk=true null=true
  name text pk=false null=true
  skill_id text pk=false null=true
  skipped_files integer pk=false null=true
  index idx_skill_versions_skill_id unique=false columns=skill_id
table skills
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
			"若这不是：说明你改了 internal/database/entity 却没意识到。\n"+
			"--- 实际 ---\n%s\n--- 期望 ---\n%s", got, wantSchema)
	}
}

// schemaDump 把库结构渲染成稳定的文本：表按名排序，列与索引各自按名排序。
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
			primary := knownFlag(t, table, column.Name(), "主键标记", column.PrimaryKey)
			nullable := knownFlag(t, table, column.Name(), "可空标记", column.Nullable)
			lines = append(lines, fmt.Sprintf("%s %s pk=%t null=%t",
				column.Name(), column.DatabaseTypeName(), primary, nullable))
		}
		sort.Strings(lines)

		indexes, err := db.Migrator().GetIndexes(table)
		if err != nil {
			t.Fatalf("读取表 %s 的索引失败: %v", table, err)
		}
		indexLines := make([]string, 0, len(indexes))
		for _, index := range indexes {
			// sqlite 为复合主键生成 sqlite_autoindex_*。它不是这里要冻结的结构：
			// 主键已经由列的 pk 标记表达，而自动索引的名字会随引擎版本漂移。
			if strings.HasPrefix(index.Name(), "sqlite_autoindex_") {
				continue
			}
			primary := knownFlag(t, table, index.Name(), "索引主键标记", index.PrimaryKey)
			if primary {
				continue
			}
			unique := knownFlag(t, table, index.Name(), "索引唯一标记", index.Unique)
			indexLines = append(indexLines, fmt.Sprintf("index %s unique=%t columns=%s",
				index.Name(), unique, strings.Join(index.Columns(), ",")))
		}
		sort.Strings(indexLines)

		fmt.Fprintf(&b, "table %s\n", table)
		for _, line := range lines {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		for _, line := range indexLines {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	return b.String()
}

// knownFlag 解开 gorm 的"值 + 是否已知"二元返回。
//
// 驱动答不上来时不能默默记成 false：快照里一个假的 false 比缺一行更糟——
// 它会让"结构变了"看起来像"结构没变"。
func knownFlag(t *testing.T, table, object, what string, read func() (bool, bool)) bool {
	t.Helper()
	value, known := read()
	if !known {
		t.Fatalf("驱动没有给出 %s.%s 的%s", table, object, what)
	}
	return value
}
