package main

import (
	"github.com/spf13/cobra"
)

// version 由构建时注入：make build 会带上 -ldflags。
var version = "dev"

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
