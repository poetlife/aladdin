package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0018Registration 建立注册策略表与邀请码表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 它是纯新增——这两张表此前不存在，没有任何存量行要搬。因此这条迁移是"建表，
// 结束"，与 0002、0012、0013、0016 同形。
//
// **不写入任何策略行。** 没有记录时的取值就是缺省姿态（开放注册、无默认角色、
// 全局范围），也就是这张表出现之前的行为——把初值写进迁移等于把同一件事说两遍，
// 而两处迟早会有一处被改（见 docs/design/identity/registration.md）。
var migration0018Registration = &gormigrate.Migration{
	ID: "0018_registration",
	Migrate: func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&database.RegistrationPolicyRecord{}); err != nil {
			return err
		}
		return tx.AutoMigrate(&database.RegistrationInviteRecord{})
	},
}
