package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0008ScopesTable 给作用域一张登记表，并**回填既有部署里已经在用的范围**。
//
// 在此之前，作用域只是绑定上的一个字符串，没有任何地方登记"这个部署里有哪些
// 范围"。有了登记表之后，绑定必须指向已登记的范围（见 docs/design/rbac/scopes.md），
// 因此不补登历史数据的话，升级后那些绑定会指向"未登记的范围"——它们仍然有效
// （判定不读登记表），但界面上的范围目录会漏掉实际在用的范围。
//
// 回填两个来源：绑定上的范围，以及主体的默认作用域。两者都是部署里"已经在用"
// 的证据。全局（空串）不登记：它是模型的根，不是一条记录。
//
// 读回全部行再在 Go 侧去重，而不是写 `SELECT DISTINCT ... ON CONFLICT DO NOTHING`：
// 后者的写法在 SQLite 与 MySQL 上不同，而这一趟是一次性的迁移，多读几行的代价
// 远小于一句只能在一种库上跑通的 SQL。
//
// **已经发布过的迁移不得修改。** 需要再改结构时追加新的。
var migration0008ScopesTable = &gormigrate.Migration{
	ID: "0008_scopes_table",
	Migrate: func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&database.ScopeRecord{}); err != nil {
			return err
		}

		var bindingRecs []database.RoleBindingRecord
		if err := tx.Find(&bindingRecs).Error; err != nil {
			return err
		}
		var subjectRecs []database.SubjectRecord
		if err := tx.Find(&subjectRecs).Error; err != nil {
			return err
		}

		seen := map[string]bool{}
		register := func(path string) error {
			if path == "" || seen[path] {
				return nil
			}
			seen[path] = true
			// 显示名留空：界面回落到显示路径本身，改名由管理员在界面上做。
			return tx.Create(&database.ScopeRecord{Path: path}).Error
		}
		for _, rec := range bindingRecs {
			if err := register(rec.Scope); err != nil {
				return err
			}
		}
		for _, rec := range subjectRecs {
			if err := register(rec.DefaultScope); err != nil {
				return err
			}
		}
		return nil
	},
}
