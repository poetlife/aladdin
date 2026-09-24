package rbac

import (
	"context"
	"errors"
	"fmt"
)

// EnsureBuiltinRoles 把权限目录派生的内置角色补进存储，返回新增的条数。
//
// 它只补**缺失**的，不覆盖已存在的：内置角色的权限绑定在部署时**可以**
// 调整（见 docs/design/rbac/role-model.md），每次启动都按代码里的定义
// 覆盖一遍，等于把这种调整静默撤销——而那正是"部署时可调整"这句承诺
// 反悔的样子。
//
// 它与迁移不是一回事：迁移管库结构，它管初始数据。因此它放在领域包里，
// 对任何存储实现都成立，而不是写在某个具体的存储实现里。
func EnsureBuiltinRoles(ctx context.Context, s MutableStore) (int, error) {
	created := 0
	for _, builtin := range BuiltinRoles {
		_, err := s.Role(ctx, builtin.ID)
		switch {
		case err == nil:
			continue
		case errors.Is(err, ErrRoleNotFound):
			// 缺失，需要补。
		default:
			return created, fmt.Errorf("读取内置角色 %s 失败: %w", builtin.ID, err)
		}
		if err := s.PutRole(ctx, builtin); err != nil {
			return created, fmt.Errorf("写入内置角色 %s 失败: %w", builtin.ID, err)
		}
		created++
	}
	return created, nil
}
