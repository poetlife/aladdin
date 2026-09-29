package migrate

import (
	"context"
	"sort"
	"testing"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"
)

// TestContentSlotsMigrationBackfillsLegacyRows 造一个"跑过旧版 0007"的库，跑 0011，
// 断言存量行被无损翻译成内容槽。
//
// 为什么不能在真库上验：迁移体用的是 AutoMigrate，它按**当前**模型建表，因此
// 一个全新的库走完 0007 之后根本没有 form 这一列（也就没有存量要搬）。旧形状
// 只能手工造出来——这正是这条测试存在的理由，0011 的两种分支各要有一次真实的
// 执行。
func TestContentSlotsMigrationBackfillsLegacyRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// 一、把库停在 0010。
	if err := gormigrate.New(db, migrationOptions(), migrations()[:10]).Migrate(); err != nil {
		t.Fatalf("跑到 0010 失败: %v", err)
	}

	// 二、把 galaxy 的三处改回旧形状：工程行上有形态与发布指针，草稿表的主键
	// 只有工程标识，版本/发布/凭证都没有 slot 列（后三者已经是旧形状）。
	legacy := []string{
		`ALTER TABLE galaxy_projects ADD COLUMN form text`,
		`ALTER TABLE galaxy_projects ADD COLUMN current_publication_id text`,
		`DROP TABLE galaxy_drafts`,
		`CREATE TABLE galaxy_drafts (
			project_id text PRIMARY KEY,
			manifest text,
			updated_at datetime
		)`,
	}
	for _, statement := range legacy {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("造旧形状失败（%s）: %v", statement, err)
		}
	}

	// 三个工程：一个站点、一个文档、一个"站点但草稿里已经有 docs/ 路径"的
	// ——最后一个用来走一遍保留段的警告分支（它必须**只是警告**，不能失败）。
	seed := []string{
		`INSERT INTO galaxy_projects (id, owner_subject_id, name, description, form, current_publication_id, created_at, updated_at)
		 VALUES ('prj_site', 'sub_1', '站点', '', 'static', 'pub_site', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO galaxy_projects (id, owner_subject_id, name, description, form, current_publication_id, created_at, updated_at)
		 VALUES ('prj_docs', 'sub_1', '文档', '', 'docs', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO galaxy_projects (id, owner_subject_id, name, description, form, current_publication_id, created_at, updated_at)
		 VALUES ('prj_reserved', 'sub_1', '占段', '', 'static', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,

		`INSERT INTO galaxy_drafts (project_id, manifest, updated_at)
		 VALUES ('prj_site', '[{"path":"index.html","kind":"text","digest":"aa"}]', CURRENT_TIMESTAMP)`,
		`INSERT INTO galaxy_drafts (project_id, manifest, updated_at)
		 VALUES ('prj_docs', '[{"path":"index.md","kind":"text","digest":"bb"}]', CURRENT_TIMESTAMP)`,
		`INSERT INTO galaxy_drafts (project_id, manifest, updated_at)
		 VALUES ('prj_reserved', '[{"path":"index.html","kind":"text","digest":"cc"},{"path":"docs/readme.txt","kind":"text","digest":"dd"}]', CURRENT_TIMESTAMP)`,

		`INSERT INTO galaxy_versions (id, project_id, seq, manifest, render_rules_version, saved_at)
		 VALUES ('ver_site', 'prj_site', 1, '[{"path":"index.html","kind":"text","digest":"aa"}]', 1, CURRENT_TIMESTAMP)`,
		`INSERT INTO galaxy_versions (id, project_id, seq, manifest, render_rules_version, saved_at)
		 VALUES ('ver_docs', 'prj_docs', 1, '[{"path":"index.md","kind":"text","digest":"bb"}]', 1, CURRENT_TIMESTAMP)`,

		`INSERT INTO galaxy_publications (id, project_id, version_id, manifest, published_by_subject_id, published_at)
		 VALUES ('pub_site', 'prj_site', 'ver_site', '[{"path":"index.html","kind":"text","digest":"aa"}]', 'sub_1', CURRENT_TIMESTAMP)`,

		`INSERT INTO galaxy_preview_grants (token, project_id, subject_id, expires_at, created_at)
		 VALUES ('tok_docs', 'prj_docs', 'sub_1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
	}
	for _, statement := range seed {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("造存量数据失败（%s）: %v", statement, err)
		}
	}

	// 三、跑 0011（Run 会把它接在已应用的 0010 之后）。
	if err := Run(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 四、每个工程得到与它原来形态对应的一个槽，发布指针跟着搬过来。
	assertSlots(t, db, map[string]string{
		"prj_site":     "site",
		"prj_docs":     "docs",
		"prj_reserved": "site",
	})
	if got := slotOfProject(t, db, "prj_site"); got != "site" {
		t.Errorf("prj_site 的槽 = %q，期望 site", got)
	}
	if got := pointerOfProject(t, db, "prj_site"); got != "pub_site" {
		t.Errorf("prj_site 的发布指针 = %q，期望 pub_site（它要从工程行搬过来）", got)
	}
	if got := pointerOfProject(t, db, "prj_docs"); got != "" {
		t.Errorf("prj_docs 的发布指针 = %q，期望空", got)
	}

	// 五、草稿、版本、发布与凭证都被盖上所属工程原来那个槽，且数量不变。
	for _, tc := range []struct {
		table     string
		key       string
		value     string
		wantSlot  string
		wantCount int
	}{
		{"galaxy_drafts", "project_id = 'prj_site'", "prj_site", "site", 3},
		{"galaxy_versions", "id = 'ver_site'", "ver_site", "site", 2},
		{"galaxy_versions", "id = 'ver_docs'", "ver_docs", "docs", 2},
		{"galaxy_publications", "id = 'pub_site'", "pub_site", "site", 1},
		{"galaxy_preview_grants", "token = 'tok_docs'", "tok_docs", "docs", 1},
	} {
		if got := slotOfRow(t, db, tc.table, tc.key); got != tc.wantSlot {
			t.Errorf("%s 里 %s 的槽 = %q，期望 %q", tc.table, tc.value, got, tc.wantSlot)
		}
		var count int64
		if err := db.Table(tc.table).Count(&count).Error; err != nil {
			t.Fatalf("清点 %s 失败: %v", tc.table, err)
		}
		if int(count) != tc.wantCount {
			t.Errorf("%s 有 %d 行，期望 %d 行（迁移应当无损）", tc.table, count, tc.wantCount)
		}
	}

	// 六、工程行上不再有形态与发布指针。这里直接问表自己的列，不问 gorm 的模型
	// ——模型已经不带这两个字段了，拿它来问"列在不在"是在问一个同义反复。
	columns := projectColumns(t, db)
	for _, column := range []string{"form", "current_publication_id"} {
		if columns[column] {
			t.Errorf("galaxy_projects 上仍然有 %s 这一列（现有列：%v）", column, columnNames(columns))
		}
	}
}

// projectColumns 读 galaxy_projects 的真实列集合。
func projectColumns(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()
	var rows []struct {
		Name string
	}
	if err := db.Raw("PRAGMA table_info(galaxy_projects)").Scan(&rows).Error; err != nil {
		t.Fatalf("读取 galaxy_projects 的列失败: %v", err)
	}
	columns := make(map[string]bool, len(rows))
	for _, row := range rows {
		columns[row.Name] = true
	}
	return columns
}

func columnNames(columns map[string]bool) []string {
	names := make([]string, 0, len(columns))
	for name := range columns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func assertSlots(t *testing.T, db *gorm.DB, want map[string]string) {
	t.Helper()
	var count int64
	if err := db.Table("galaxy_project_slots").Count(&count).Error; err != nil {
		t.Fatalf("清点内容槽失败: %v", err)
	}
	if int(count) != len(want) {
		t.Errorf("内容槽有 %d 行，期望每个工程恰好一行（%d 行）", count, len(want))
	}
	for projectID, slot := range want {
		if got := slotOfProject(t, db, projectID); got != slot {
			t.Errorf("工程 %s 的槽 = %q，期望 %q", projectID, got, slot)
		}
	}
}

func slotOfProject(t *testing.T, db *gorm.DB, projectID string) string {
	t.Helper()
	return slotOfRow(t, db, "galaxy_project_slots", "project_id = '"+projectID+"'")
}

func pointerOfProject(t *testing.T, db *gorm.DB, projectID string) string {
	t.Helper()
	var pointer string
	if err := db.Table("galaxy_project_slots").
		Select("COALESCE(current_publication_id, '')").
		Where("project_id = ?", projectID).
		Scan(&pointer).Error; err != nil {
		t.Fatalf("读取 %s 的发布指针失败: %v", projectID, err)
	}
	return pointer
}

func slotOfRow(t *testing.T, db *gorm.DB, table, condition string) string {
	t.Helper()
	var slot string
	if err := db.Table(table).Select("slot").Where(condition).Scan(&slot).Error; err != nil {
		t.Fatalf("读取 %s（%s）的槽失败: %v", table, condition, err)
	}
	return slot
}
