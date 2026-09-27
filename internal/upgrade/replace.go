package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
)

// Replace 原子地把 path 指向的那个文件换成 content。
//
// 两条性质（见 docs/design/cli/self-update.md）：
//
//   - **原子**：从任何观察者的角度看，那个文件要么是旧的、要么是新的；失败的
//     替换不改变它的任何字节。写到一个同目录的临时文件再改名是达成它的方式
//     ——改名在同一个文件系统内是单次操作，跨文件系统则不是，所以临时文件
//     必须落在目标所在的那个目录里。
//   - **按真实文件**：path 是符号链接时，换的是链接**指向的那个文件**，而不是
//     把链接本身换成一份普通文件。把链接换掉会无声改掉使用者的目录布局。
func Replace(path string, content []byte) error {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("无法解析 %s：%w", path, err)
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".aladdin-update-*")
	if err != nil {
		return fmt.Errorf("无法在 %s 下创建临时文件（这个目录可写吗？）：%w", dir, err)
	}
	tmpName := tmp.Name()
	// 任何一条失败路径都要清掉临时文件：在别人的可执行目录里留下一个半成品，
	// 比这次升级失败本身更糟。
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入临时文件失败：%w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置可执行权限失败：%w", err)
	}
	// 先落盘再改名：否则一次断电可能让改名后的文件是空的，而被替换掉的那份
	// 已经不在原处了。
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("落盘失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败：%w", err)
	}

	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("替换 %s 失败：%w", target, err)
	}
	// 改名成功之后临时文件已经不存在于那个名字上，不要再删。
	tmpName = ""
	return nil
}
