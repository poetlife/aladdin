// Package buildinfo 回答"这份二进制是哪次构建"。
//
// 它是**构建期注入的唯一落点**：服务端与命令行不再各自声明一份版本变量，
// Makefile 的 LDFLAGS 也只有一个注入目标（见 docs/design/deployment/README.md）。
// 这件事原本有两份——`main.version` 在 cmd/aladdin 与 cmd/aladdin-server 里各写
// 一次——两份的漂移方式很隐蔽：命令行报的版本与它连的服务端报的版本同源，
// 而这件事只靠"两处 -ldflags 长得一样"维持。
//
// 没注入时各字段退化成诚实的缺省值（`dev`、空提交号、零值构建时间），而不是
// 编一个看起来像真的版本号出来。构建期是否真的注入成功由 Makefile 与
// makefile_test.go 一起保证。
package buildinfo

import (
	"os"
	"runtime"
	"time"
)

// 以下四项由构建期经 -ldflags -X 注入。
//
// **不得在别处再声明一份**：多一处就多一个会漂的版本号，而两个版本号不一致的
// 表现是"命令行说的版本和页面上说的不一样"，排查时无从判断该信哪个。
var (
	// Version 是版本号：发布构建为 tag（vX.Y.Z），本机构建为 git describe 的
	// 结果（v0.4.3-5-gabc1234-dirty），不在仓库里时为 dev。
	Version = "dev"
	// Commit 是构建时的提交号。不在仓库里构建时为空。
	Commit = ""
	// BuildTime 是构建时刻，RFC3339（UTC）。本机构建由 Makefile 取当时的时间。
	BuildTime = ""
	// Released 是"这份二进制来自发布产物"的标记，只由 make release-build 注入。
	//
	// 自更新只认它，不认版本号的形态：在恰好处于某个 tag 的干净工作树上，
	// 本机构建注入的版本号也是一个合法的 vX.Y.Z（见 internal/upgrade）。
	Released = ""
)

// startedAt 是**本进程开始运行的时刻**。
//
// 取包初始化那一刻而不是监听成功那一刻：前者是"进程起来了"这件事的最近似值，
// 后者会漏掉迁移、回收这些在监听之前跑掉的启动工作——而排障时"这次发布到底
// 是几点起来的"问的正是整段启动。
//
// 它是进程内的一次取值，不是配置、不从外部来（见 AGENTS.md 第 7 条）。
var startedAt = time.Now().UTC()

// instance 是本进程所在主机的名字。
//
// 多副本部署时页面上的这一项就是"我看的是哪一个副本"。取不到（无 hostname 的
// 极简容器）时留空——留空是可判定的，编一个随机标识则不可判定。
var instance = readHostname()

func readHostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

// Info 是一个部署实例在某一刻的自述。
//
// 它只描述**这一次构建与这一个进程**，不含任何配置取值：库地址、桶地址、
// 密钥一律不在这里（配置里有什么见 docs/design/config/README.md）。运维页要
// 回答的是"线上跑的是哪个版本"，不是"这份配置长什么样"。
type Info struct {
	Version string
	Commit  string
	// BuildTime 为零值表示构建期没有注入时间。
	BuildTime time.Time
	Released  bool
	GoVersion string
	OS        string
	Arch      string
	// Instance 是主机名；空表示取不到。
	Instance string
	// StartedAt 是本进程启动的时刻（UTC）。
	StartedAt time.Time
}

// Read 返回当前进程的构建与运行信息。
//
// 它是**只读的**：没有任何一项可以被调用方改写，因此同一次运行里多次读取给出
// 的除 uptime 外完全一致——页面上的 uptime 由客户端按 StartedAt 自己算，不靠
// 反复拉取接口。
func Read() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: parseBuildTime(BuildTime),
		// 与 Makefile 注入的那一个字面量同一个判断（见 internal/upgrade）。
		Released:  Released == "true",
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Instance:  instance,
		StartedAt: startedAt,
	}
}

// parseBuildTime 把注入的时刻解析成时间；无法解析时返回零值。
//
// **不失败**：构建时间是给人看的附注，一个格式不对的时间不该让整页部署信息
// 读不出来。零值在协议里落成空串，界面据此显示"未知"。
func parseBuildTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
