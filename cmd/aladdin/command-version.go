package main

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/poetlife/aladdin/internal/buildinfo"
)

// version 命令报的版本号来自 internal/buildinfo——与 aladdin-server 报的是同一个
// 变量，因此"命令行说的版本"与"部署信息页上说的版本"不可能不一致。
//
// 这里刻意不留一份本地副本：`make build` 与 `make release-build` 都只往
// internal/buildinfo 注入（见 Makefile 的 LDFLAGS），多一份本地变量就多一个会漂的
// 版本号，而两个版本号不一致时无从判断该信哪个。
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本号",
		Long: `打印本机命令行的版本号。

它与服务端的部署信息页报的是同一个构建期注入值（见 docs/design/deployment/README.md），
因此拿它和页面上那一栏对照，就能判断命令行与它连的服务端是不是同一次发布。

只有构建期确实注入过的字段会打印：提交号与构建时间在 go build、go install
这类不经 Makefile 的构建下是空的，那时只给版本号。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := buildinfo.Read()
			if flags.output == "json" {
				out := map[string]string{
					"version":  info.Version,
					"commit":   info.Commit,
					"released": releasedText(info.Released),
				}
				// 没注入构建时间时给空串，而不是编一个看起来像真的时间戳。
				if !info.BuildTime.IsZero() {
					out["build_time"] = info.BuildTime.Format(time.RFC3339)
				}
				return printJSON(out)
			}

			println(cmd.OutOrStdout(), versionLine(info))
			return nil
		},
	}
}

// versionLine 组装人读的那一行。
//
// 缺的字段直接不出现，不占位也不留空格：`dev （提交号未知）` 这种写法把"没有"
// 和"没记"混成了同一句话，而这一行的读法应当是"看到什么就是什么"。发布构建
// 三项齐全，本机构建常常只有第一项。
func versionLine(info buildinfo.Info) string {
	parts := []string{info.Version}
	if info.Commit != "" {
		parts = append(parts, info.Commit)
	}
	if !info.BuildTime.IsZero() {
		parts = append(parts, info.BuildTime.Format(time.RFC3339))
	}
	return strings.Join(parts, " ")
}

// releasedText 把发布标记落成 JSON 里的取值。
//
// 用字符串而不是布尔：同一个 map 里另外几项都是字符串，混一个 bool 进去会让
// `aladdin version --output json` 的消费方（jq 之外还有脚本）需要按字段分别取值。
func releasedText(released bool) string {
	if released {
		return "true"
	}
	return "false"
}
