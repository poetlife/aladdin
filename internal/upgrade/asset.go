package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

const (
	// maxArchiveBytes 是产物包的读取上界。
	maxArchiveBytes = 64 << 20
	// maxBinaryBytes 是解包出来的二进制的上界。
	maxBinaryBytes = 128 << 20
)

// SupportedPlatforms 列出发布流水线产出命令行产物的平台。
//
// 它只用在一处：本平台没有产物时把它写进错误信息。发布要加平台只需改
// Makefile 的 PLATFORMS，这里是那份清单的**消费侧**——两者不一致的代价只是
// 一句措辞不准，不会让任何一次升级走错路（见 docs/release.md）。
var SupportedPlatforms = []string{"linux/amd64", "darwin/arm64"}

// assetName 是本平台命令行产物的名字。
//
// 这个名字是**对外契约**的一部分：发布流水线用它打包，自更新用它挑选
// （见 docs/release.md）。改一处而不改另一处，表现为已经装出去的命令行
// 找不到产物。
func assetName(tag, goos, goarch string) string {
	return "aladdin_" + tag + "_" + goos + "_" + goarch + ".tar.gz"
}

// extractBinary 从产物包里取出那个二进制。
//
// 产物包的结构也是对外契约的一部分：二进制放在包的根、且是包里唯一的条目。
// 这里只接受"根下的一个普通文件"——不接受目录、符号链接、或带路径前缀的
// 条目。一个能写出 ../ 的包不该被解到本机的任意位置。
func extractBinary(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("产物不是合法的 gzip：%w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var binary []byte
	entries := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("产物不是合法的 tar：%w", err)
		}
		entries++
		if entries > 1 {
			return nil, errors.New("产物里有不止一个条目")
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("产物的条目 %q 不是普通文件", header.Name)
		}
		if header.Name != filepath.Base(header.Name) {
			return nil, fmt.Errorf("产物的条目 %q 带了路径", header.Name)
		}
		if header.Size > maxBinaryBytes {
			return nil, errors.New("产物里的二进制超出了大小上界")
		}
		binary, err = io.ReadAll(io.LimitReader(tr, maxBinaryBytes))
		if err != nil {
			return nil, fmt.Errorf("读取产物内容失败：%w", err)
		}
	}

	if len(binary) == 0 {
		return nil, errors.New("产物里没有二进制")
	}
	return binary, nil
}
