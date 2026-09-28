package main

import (
	"io"
	"os"
)

// contentStdinPath 是 --file 的特殊取值：从标准输入读正文。
const contentStdinPath = "-"

// readContent 读出要交给服务端的正文。
//
// 它是正文输入的**唯一入口**：`draft save` 与 `validate` 都调它，因此"校验通过
// 的那份字节"与"存进草稿的那份字节"必然是同一份（见 docs/design/galaxy/cli.md）。
//
// 读不到按**用法错误**处理：给错的路径与给错的参数同属"调用方输入不合法"，
// 脚本据此得到一个可分支的退出码，而不是与内部故障混在一起。
func readContent(path string) (string, error) {
	if path == "" {
		return "", usageErrorf("必须用 --file 指明正文来源（- 表示从标准输入读取）")
	}
	var (
		data []byte
		err  error
	)
	if path == contentStdinPath {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path) //nolint:gosec // 路径来自调用者自己的命令行参数，读的是他自己的文件
	}
	if err != nil {
		return "", usageErrorf("读取正文失败: %s", err)
	}
	return string(data), nil
}
