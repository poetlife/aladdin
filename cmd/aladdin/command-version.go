package main

import (
	"github.com/spf13/cobra"
)

// version 由构建时注入：make build 会带上 -ldflags。
var version = "dev"

// released 是"这份二进制来自发布产物"的标记，只由 make release-build 注入。
//
// 自更新只认它，不认版本号的形态：在恰好处于某个 tag 的干净工作树上，
// make build 注入的版本号也是一个合法的 vX.Y.Z（见 internal/upgrade）。
var released = ""

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本号",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.output == "json" {
				return printJSON(map[string]string{"version": version})
			}
			println(cmd.OutOrStdout(), version)
			return nil
		},
	}
}
