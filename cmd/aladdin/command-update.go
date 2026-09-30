package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/upgrade"
)

func newUpdateCommand() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "把本机命令行升级到最新发布版本",
		Long: `从发布取回产物、校验、替换本机正在运行的这个二进制。

只支持**由发布产物安装**的二进制：本机构建（make build、go install）不带发布
流水线注入的标记，一律拒绝——把某人的工作副本无声换成一份发布产物不是升级。
升级源不可配置：本仓库的 GitHub 发布是权威源，官方站点上另有一份"只留最新"的
镜像，**仅在发布源暂时不可用**（限频、5xx、网络不通）时兜底。

只有这条命令会访问网络；其它命令（含 version）都不发任何出站请求。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "只检查有没有新版本，不下载也不替换")
	return cmd
}

func runUpdate(cmd *cobra.Command, checkOnly bool) error {
	updater, err := upgrade.New(upgrade.Options{
		Current:  version,
		Released: released != "",
		// 兜底镜像挂在官方站点上，与"这次连哪个服务端"无关：用隧道连本机的人
		// 也应当从官方站点取升级材料。源码构建恒为空（没有官方地址可推导）。
		MirrorOrigin: config.OfficialSiteOrigin(),
	})
	if err != nil {
		if errors.Is(err, upgrade.ErrNotReleased) {
			// 拒绝的理由要说清楚：当前是什么版本、为什么不能自更新、该怎么做。
			return fmt.Errorf(
				"当前版本 %q 不是发布产物，自更新只支持从发布页安装的二进制。\n"+
					"本机构建的请重新构建，从发布页下载的请重新下载", version)
		}
		return err
	}

	// 不套用 --timeout：它是单次 RPC 调用的上限，而这里的一次下载本就比一次
	// RPC 慢。出站请求各自有时间上限（见 internal/upgrade）。
	ctx := context.Background()

	decision, err := updater.Resolve(ctx)
	if err != nil {
		return err
	}

	// 回退真的发生时要说清楚：用户看到的版本信息不是从权威源来的。
	if decision.Source == upgrade.SourceMirror && flags.output != "json" {
		printf(cmd.OutOrStdout(), "发布源不可用，本次经主站镜像\n")
	}

	switch decision.Status {
	case upgrade.StatusUpToDate:
		if flags.output == "json" {
			return printJSON(updateResult(decision, "up_to_date"))
		}
		printf(cmd.OutOrStdout(), "已是最新版本 %s\n", decision.Current)
		return nil

	case upgrade.StatusAhead:
		if flags.output == "json" {
			return printJSON(updateResult(decision, "ahead"))
		}
		printf(cmd.OutOrStdout(),
			"本机版本 %s 高于最新发布 %s，不降级\n", decision.Current, decision.Latest)
		return nil
	}

	if checkOnly {
		if flags.output == "json" {
			return printJSON(updateResult(decision, "update_available"))
		}
		printf(cmd.OutOrStdout(), "有新版本：%s → %s\n", decision.Current, decision.Latest)
		return nil
	}

	result, err := updater.Upgrade(ctx, decision)
	if err != nil {
		return err
	}
	if flags.output == "json" {
		// 只报"从哪换到了哪"：新装上的版本是 to，from 是升级前那个进程的版本。
		return printJSON(map[string]string{
			"status": "updated",
			"from":   result.From.String(),
			"to":     result.To.String(),
			"path":   result.Path,
			// 这次的材料是从哪个源取的：回退发生时，答案不是权威源给的。
			"source": decision.Source.Slug(),
		})
	}
	printf(cmd.OutOrStdout(),
		"已升级 %s → %s（%s）\n当前进程仍是旧版本，新版本在下次执行时生效\n",
		result.From, result.To, result.Path)
	return nil
}

// updateResult 组装只报告形态的 JSON 输出。
func updateResult(decision upgrade.Decision, status string) map[string]string {
	return map[string]string{
		"status":  status,
		"current": decision.Current.String(),
		"latest":  decision.Latest.String(),
		"source":  decision.Source.Slug(),
	}
}
