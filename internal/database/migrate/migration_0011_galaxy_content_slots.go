package migrate

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0011GalaxyContentSlots 把「一个工程一种形态」改成「一个工程一组内容槽」。
//
// 形态（static / docs）从工程行上的一列变成**内容槽表上的一行**，发布指针也从
// 工程行搬到那一行上（每个槽一个）；草稿的主键从工程标识变成（工程标识，槽）；
// 版本、发布记录与预览凭证各加一列 slot。
//
// **它是无损的，与 0007 那一课正相反。** 旧形态直接回答"这个工程原来有哪一个
// 槽"：`static` 得到一个 site 槽，`docs` 得到一个 docs 槽，两者一一对应。因此
// 存量行不必丢弃——每一条草稿、版本、发布与凭证都被盖上它所属工程原来那个槽，
// 数量与内容都不变。这也正是"加一个槽不会让历史版本无法复现"在数据上的样子：
// 每个槽自带一串版本。
//
// **它同时面对两种库，因此每一步都先问"那一列在不在"。** 迁移体用的是
// AutoMigrate，它按**当前**模型建表：一个全新的库走完 0007 之后，工程行上根本
// 没有 form 这一列，草稿表也已经是新主键。那种库里没有存量行可搬，跳过回填
// 即可；只有"跑过旧版 0007 的库"才需要逐行翻译。不区分这两种情形的话，全新
// 的库会在回填那一步报"没有 form 这一列"。
//
// 一条要说清的影响面：**`docs` 从此是文档槽占用的保留段**（见
// docs/design/galaxy/site-model.md 的"地址"），因此 site 槽里那些以 `docs/` 开头
// 的路径会改指文档槽、从而不可达。迁移**检测并留痕**这类路径（走 gorm 的日志，
// 它已接到本进程的 zap 上），但不因此失败——那会让一次结构升级卡在一个历史
// 路径上。galaxy 上线同日，真实存量按 0007 的记载是零行。
//
// **已经发布过的迁移不得修改。** 需要再改结构时追加新的。
var migration0011GalaxyContentSlots = &gormigrate.Migration{
	ID: "0011_galaxy_content_slots",
	Migrate: func(tx *gorm.DB) error {
		// 一、建内容槽表。它是"这个工程有哪些内容"的**唯一**定义处。
		if err := tx.AutoMigrate(&database.GalaxySlotRecord{}); err != nil {
			return err
		}

		// 二、给三张已有表加 slot 列（记录类型已经带上它，AutoMigrate 只补列）。
		if err := tx.AutoMigrate(
			&database.GalaxyVersionRecord{},
			&database.GalaxyPublicationRecord{},
			&database.GalaxyPreviewGrantRecord{},
		); err != nil {
			return err
		}

		// 工程行上还有 form，说明这是一个跑过旧版 0007 的库：存量行要翻译。
		legacyForm := hasColumn(tx, "galaxy_projects", "form")
		// 草稿表还没有 slot 列，说明它的主键还是旧形状：要重建。
		legacyDrafts := !hasColumn(tx, "galaxy_drafts", "slot")

		if legacyForm {
			// 三、回填 slot 列：每个工程的旧形态决定它落到哪一个槽。落不上工程的
			// 孤儿行（正常路径下不存在）按 site 处理，而不是留一个空槽——空槽
			// 不是一个合法取值。
			for _, table := range []string{"galaxy_versions", "galaxy_publications", "galaxy_preview_grants"} {
				statement := `UPDATE ` + table + ` SET slot = COALESCE((
					SELECT CASE WHEN p.form = 'docs' THEN 'docs' ELSE 'site' END
					FROM galaxy_projects p WHERE p.id = ` + table + `.project_id), 'site')
					WHERE slot IS NULL OR slot = ''`
				if err := tx.Exec(statement).Error; err != nil {
					return err
				}
			}

			// 四、由工程建出各自的槽行（含发布指针：它从工程行搬到这里）。
			if err := tx.Exec(`INSERT INTO galaxy_project_slots (project_id, slot, current_publication_id)
				SELECT id, CASE WHEN form = 'docs' THEN 'docs' ELSE 'site' END, current_publication_id
				FROM galaxy_projects`).Error; err != nil {
				return err
			}
		}

		// 五、草稿表的主键从工程标识变成（工程标识，槽）——**主键变更 AutoMigrate
		// 做不到**，只能重建这一张表并把存量行搬过去。
		if legacyDrafts {
			if err := rebuildDrafts(tx); err != nil {
				return err
			}
		}

		// 六、检测保留段的影响面（见上面的说明）。只留痕，不失败。
		warnReservedPaths(tx)

		// 七、工程行上不再有形态与发布指针：两样都住在内容槽表上。放在最后，
		// 因为上面的回填还要读 form。
		//
		// 删列走**一句显式的 DDL**，不用 gorm 的 Migrator().DropColumn：后者在
		// 模型里找不到对应字段时的行为依赖驱动（这一处的字段本来就已经从模型里
		// 删掉了），静默不删是一种最难发现的偏差。列在不在由上面那个 hasColumn
		// 回答。
		for _, column := range []string{"form", "current_publication_id"} {
			if !hasColumn(tx, "galaxy_projects", column) {
				continue
			}
			if err := tx.Exec("ALTER TABLE galaxy_projects DROP COLUMN " + column).Error; err != nil {
				return err
			}
		}
		return nil
	},
}

// hasColumn 判定一张表上有没有这一列。
//
// **不用 gorm 的 Migrator().HasColumn。** 它在 sqlite 上靠 information_schema
// 查，而那张表不存在——查询失败被它自己吞掉，于是**任何列都答"没有"**。拿它
// 做守卫，整段回填会被静默跳过，而"跳过"与"做完了"在结果上难以区分。这里读
// 驱动给出的列清单（两个后端都实现得了，结构快照测试用的也是它）。
func hasColumn(tx *gorm.DB, table, column string) bool {
	types, err := tx.Migrator().ColumnTypes(table)
	if err != nil {
		return false
	}
	for _, columnType := range types {
		if columnType.Name() == column {
			return true
		}
	}
	return false
}

// rebuildDrafts 把草稿表换成 (工程标识, 槽) 复合主键的版本，并搬运存量行。
//
// 每一行按它所属工程原来的形态落槽——旧表上只有工程标识，槽只能从工程行读。
func rebuildDrafts(tx *gorm.DB) error {
	const oldTable = "galaxy_drafts_old"
	if tx.Migrator().HasTable(oldTable) {
		if err := tx.Migrator().DropTable(oldTable); err != nil {
			return err
		}
	}
	if err := tx.Migrator().RenameTable("galaxy_drafts", oldTable); err != nil {
		return err
	}
	if err := tx.AutoMigrate(&database.GalaxyDraftRecord{}); err != nil {
		return err
	}
	err := tx.Exec(`INSERT INTO galaxy_drafts (project_id, slot, manifest, updated_at)
		SELECT d.project_id,
		       CASE WHEN p.form = 'docs' THEN 'docs' ELSE 'site' END,
		       d.manifest, d.updated_at
		FROM galaxy_drafts_old d
		LEFT JOIN galaxy_projects p ON p.id = d.project_id`).Error
	if err != nil {
		return err
	}
	return tx.Migrator().DropTable(oldTable)
}

// manifestEntryPath 只取清单里一条条目的路径——迁移只关心路径，不关心它指向什么。
type manifestEntryPath struct {
	Path string `json:"path"`
}

// warnReservedPaths 找出站点槽里占用保留段的路径，并留一条痕。
//
// **它只警告，不失败。** 这些路径在迁移之后会改指文档槽、从而不可达，而拒绝
// 启动会让一次结构升级卡在一个历史路径上——那是更坏的结果。
func warnReservedPaths(tx *gorm.DB) {
	if tx.Logger == nil {
		return
	}
	ctx := context.Background()
	type row struct {
		ProjectID string
		Manifest  string
	}
	for _, table := range []string{"galaxy_drafts", "galaxy_versions"} {
		var rows []row
		if err := tx.Table(table).
			Select("project_id, manifest").
			Where("slot = ?", "site").
			Scan(&rows).Error; err != nil {
			// 检测失败不影响迁移的正确性：它只是留痕。
			tx.Logger.Warn(ctx, "迁移 0011 检测保留段失败", "table", table, "error", err)
			continue
		}
		for _, r := range rows {
			var entries []manifestEntryPath
			if err := json.Unmarshal([]byte(r.Manifest), &entries); err != nil {
				continue
			}
			for _, entry := range entries {
				if reservedPath(entry.Path) {
					tx.Logger.Warn(ctx,
						"迁移 0011：站点槽里存在以 docs 开头的路径，它此后会改指文档槽而不对外可达",
						"table", table, "project_id", r.ProjectID, "path", entry.Path)
					break
				}
			}
		}
	}
}

// reservedPath 判定一条路径是否落在文档槽占用的那一段里。
func reservedPath(entryPath string) bool {
	const reserved = "docs"
	return entryPath == reserved ||
		(len(entryPath) > len(reserved) && entryPath[:len(reserved)+1] == reserved+"/")
}
