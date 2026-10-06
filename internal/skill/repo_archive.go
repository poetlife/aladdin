package skill

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// 本文件把远端给的**压缩包**读成一棵文件树。它是纯函数（输入一个 reader，输出
// 文件列表），因此可以完整地离线测——而那正是要紧的：路径逃逸、链接、超限这三类
// 问题都只在这里挡得住，放过去就到了包校验那一层，而那一层看到的已经是一组
// "看着正常的路径"（见 docs/design/skill/onboarding.md 的"取回"）。

// readRepoArchive 读一个仓库压缩包，取出 subPath 之内的文件（唯一入口）。
//
// 三件事在流上做，**不落地到磁盘**：
//
//   - 只保留目标子路径下的条目。收缩发生在解压过程中，因此"子路径之外有多大"
//     不进入内存；
//   - 累计字节数有上限（MaxExpandedBytes）。压缩包本身的上限由调用方按读长度
//     施加，两个上限一起管住最坏内存；
//   - 符号链接与硬链接**直接拒绝**，不是跳过。包契约要求"一棵树"（见
//     docs/design/skill/onboarding.md），而链接把树变成一张图——静默跳过会让
//     一个依赖链接的仓库纳管进来少几个文件，而没人看得出来。
func readRepoArchive(r io.Reader, subPath string) ([]FetchedFile, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: 压缩包读不开: %w", ErrRemoteUnavailable, err)
	}
	defer func() { _ = gz.Close() }()

	reader := tar.NewReader(gz)
	var (
		files    []FetchedFile
		expanded int64
	)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: 压缩包解析失败: %w", ErrRemoteUnavailable, err)
		}

		relative, keep, err := archiveEntryPath(header, subPath)
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}

		expanded += header.Size
		if expanded > MaxExpandedBytes {
			return nil, fmt.Errorf("%w: 解压后的字节数超过上限 %d", ErrRemoteUnavailable, MaxExpandedBytes)
		}
		data, err := io.ReadAll(io.LimitReader(reader, header.Size))
		if err != nil {
			return nil, fmt.Errorf("%w: 读取压缩包条目失败: %w", ErrRemoteUnavailable, err)
		}
		files = append(files, FetchedFile{Path: relative, Data: data})
	}
	return files, nil
}

// archiveEntryPath 判定一条压缩包条目要不要收，并给出它在包内的路径。
//
// 仓库压缩包的第一段是归档根（GitHub 用 `<owner>-<repo>-<sha>`）。**它不是包
// 的一部分**，因此一律剥掉：留着它会让每一条路径都以一个随提交变化的字符串开头，
// 而包是一棵**与来源无关**的树。
func archiveEntryPath(header *tar.Header, subPath string) (string, bool, error) {
	name := strings.TrimPrefix(header.Name, "./")
	// 归档里偶尔带一个显式的根目录条目，它剥掉第一段之后什么都不剩。
	trimmed := strings.TrimSuffix(name, "/")

	switch header.Typeflag {
	case tar.TypeDir:
		return "", false, nil
	case tar.TypeSymlink, tar.TypeLink:
		return "", false, fmt.Errorf("%w: 压缩包里含链接 %q，技能包必须是一棵自足的树",
			ErrPackageInvalid, name)
	case tar.TypeReg:
		// 归档里的零值型的普通文件由标准库的读取器归一成 TypeReg（见
		// archive/tar 的 Reader.next），因此这里不必再列一次。
	default:
		// 设备节点、管道、字符设备：都不该出现在一个技能包里。
		return "", false, fmt.Errorf("%w: 压缩包里含非常规条目 %q",
			ErrPackageInvalid, name)
	}

	if trimmed == "" {
		return "", false, nil
	}
	// 先归一化再判逃逸：`a/../../etc` 这类取值在 Clean 之后才现形。归一到以 `/`
	// 开头的取值同样要拒——那是一条绝对路径，它此后会被当成"仓库根下的一段"，
	// 而两者根本不是一回事。
	cleaned := path.Clean(trimmed)
	if strings.HasPrefix(cleaned, "/") {
		return "", false, fmt.Errorf("%w: 压缩包条目的路径 %q 是绝对路径",
			ErrPackageInvalid, name)
	}
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", false, fmt.Errorf("%w: 压缩包条目的路径 %q 逃出了归档根",
			ErrPackageInvalid, name)
	}
	segments := strings.Split(cleaned, "/")
	if len(segments) < 2 {
		// 只剩归档根自己，不是一个文件。
		return "", false, nil
	}
	relative := strings.Join(segments[1:], "/")

	if subPath != "" {
		if relative != subPath && !strings.HasPrefix(relative, subPath+"/") {
			return "", false, nil
		}
		relative = strings.TrimPrefix(strings.TrimPrefix(relative, subPath), "/")
		if relative == "" {
			return "", false, nil
		}
	}
	return relative, true, nil
}
