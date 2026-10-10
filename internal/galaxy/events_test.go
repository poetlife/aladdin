package galaxy

import (
	"context"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/watch"
)

// changed 报告这条订阅上有没有还没被取走的"变了"。
func changed(sub *watch.Subscription) bool {
	select {
	case <-sub.Ready():
		return true
	default:
		return false
	}
}

// ended 报告这条订阅是否已经结束。
func ended(sub *watch.Subscription) bool {
	select {
	case <-sub.Done():
		return true
	default:
		return false
	}
}

// **每一次改变工程状态的写入，都在这个工程的主题上留下恰好一条事件。**
//
// 这是"别处改完这一页不动"那条 bug 的防线：漏发一个写入点的表现，就是订阅者
// 永远停在旧内容上。
func TestEachWritePublishesOnTheProjectTopic(t *testing.T) {
	// stage 是各个动作需要的前置状态：一个工程，可能还有一个版本与一份资产。
	type stage struct {
		project Project
		version Version
		asset   Asset
	}

	cases := []struct {
		name    string
		prepare func(t *testing.T, f *fixture) stage
		act     func(t *testing.T, f *fixture, s stage)
	}{
		{
			name:    "改工程元数据",
			prepare: func(t *testing.T, f *fixture) stage { return stage{project: f.createProject(t, "工程")} },
			act: func(t *testing.T, f *fixture, s stage) {
				if _, err := f.service.UpdateProject(context.Background(), testOwner, s.project.ID, "新名字", "新简介"); err != nil {
					t.Fatalf("改元数据失败: %v", err)
				}
			},
		},
		{
			name:    "推送草稿",
			prepare: func(t *testing.T, f *fixture) stage { return stage{project: f.createProject(t, "工程")} },
			act: func(t *testing.T, f *fixture, s stage) {
				f.pushDraft(t, s.project.ID, []Entry{f.textEntry(t, s.project.ID, "index.html", "内容")})
			},
		},
		{
			name: "保存版本",
			prepare: func(t *testing.T, f *fixture) stage {
				return stage{project: f.staticSite(t, map[string]string{"index.html": "内容"})}
			},
			act: func(t *testing.T, f *fixture, s stage) { f.saveVersion(t, s.project.ID) },
		},
		{
			name: "删除版本",
			prepare: func(t *testing.T, f *fixture) stage {
				project := f.staticSite(t, map[string]string{"index.html": "内容"})
				return stage{project: project, version: f.saveVersion(t, project.ID)}
			},
			act: func(t *testing.T, f *fixture, s stage) {
				if err := f.service.DeleteVersion(context.Background(), testOwner, s.project.ID, SlotSite, s.version.ID); err != nil {
					t.Fatalf("删除版本失败: %v", err)
				}
			},
		},
		{
			name:    "提交资产上传",
			prepare: func(t *testing.T, f *fixture) stage { return stage{project: f.createProject(t, "工程")} },
			act: func(t *testing.T, f *fixture, s stage) {
				f.uploadAsset(t, s.project.ID, "image/png", "图.png", []byte("假的 png 字节"))
			},
		},
		{
			name: "删除资产",
			prepare: func(t *testing.T, f *fixture) stage {
				project := f.createProject(t, "工程")
				return stage{project: project, asset: f.uploadAsset(t, project.ID, "image/png", "图.png", []byte("假的 png 字节"))}
			},
			act: func(t *testing.T, f *fixture, s stage) {
				if err := f.service.DeleteAsset(context.Background(), testOwner, s.project.ID, s.asset.ID); err != nil {
					t.Fatalf("删除资产失败: %v", err)
				}
			},
		},
		{
			name: "发布",
			prepare: func(t *testing.T, f *fixture) stage {
				project := f.staticSite(t, map[string]string{"index.html": "内容"})
				return stage{project: project, version: f.saveVersion(t, project.ID)}
			},
			act: func(t *testing.T, f *fixture, s stage) { f.publish(t, s.project.ID, s.version.ID) },
		},
		{
			name: "撤回发布",
			prepare: func(t *testing.T, f *fixture) stage {
				project, version, _ := f.publishSite(t, map[string]string{"index.html": "内容"})
				return stage{project: project, version: version}
			},
			act: func(t *testing.T, f *fixture, s stage) {
				if err := f.service.Unpublish(context.Background(), testOwner, s.project.ID, SlotSite); err != nil {
					t.Fatalf("撤回发布失败: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			s := tc.prepare(t, f)
			// **在前置动作之后**订阅：前置动作本来就不该被这条订阅看到，否则
			// 下面那条"只发一条"的断言会依赖于前置步骤的数量。
			sub := f.events.Subscribe([]string{ProjectTopic(s.project.ID)})
			t.Cleanup(sub.Close)

			tc.act(t, f, s)

			if !changed(sub) {
				t.Fatal("这次写入没有在工程的主题上发出事件")
			}
			topic, _ := sub.Take()
			if topic != ProjectTopic(s.project.ID) {
				t.Errorf("事件的主题 = %q，期望 %q", topic, ProjectTopic(s.project.ID))
			}
			if changed(sub) {
				t.Error("这次写入产生了不止一条事件")
			}
		})
	}
}

// 工程被删除时：**先报一次，再让这条主题退场**。
//
// 订阅者据此去读一次、读到"不存在"，页面显示那句既有的话；此后这条主题不会再
// 有任何事件——把一条已经没有发布方的主题留在订阅集合里没有意义。
func TestDeleteProjectReportsThenRetiresTheTopic(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	sub := f.events.Subscribe([]string{ProjectTopic(project.ID)})
	t.Cleanup(sub.Close)

	if err := f.service.DeleteProject(context.Background(), testOwner, project.ID); err != nil {
		t.Fatalf("删除工程失败: %v", err)
	}

	if !changed(sub) {
		t.Fatal("删除工程没有发出事件")
	}
	// 主题退场之后，这条订阅（它只订了这一个主题）随之结束。
	if !ended(sub) {
		t.Error("工程已删除，订阅却还活着")
	}
}

// **写失败不发事件。**
//
// 两条理由：订阅者会为一次什么都没发生的操作白重拉一遍；而"写失败也发"会让
// 事件不再意味着"状态变了"，下游就没法据它做任何判断了。
func TestWritesThatFailPublishNothing(t *testing.T) {
	cases := []struct {
		name string
		act  func(t *testing.T, f *fixture, project Project)
	}{
		{
			name: "推送一份缺入口文件的清单",
			act: func(t *testing.T, f *fixture, project Project) {
				entry := f.textEntry(t, project.ID, "other.html", "内容")
				if _, err := f.service.PushDraft(context.Background(), testOwner, project.ID, SlotSite, []Entry{entry}, testDraftSource); err == nil {
					t.Fatal("static 形态缺 index.html 的清单应当被拒")
				}
			},
		},
		{
			name: "工程名称过长",
			act: func(t *testing.T, f *fixture, project Project) {
				if _, err := f.service.UpdateProject(context.Background(), testOwner, project.ID,
					strings.Repeat("字", ProjectNameMaxRunes+1), ""); err == nil {
					t.Fatal("过长的工程名称应当被拒")
				}
			},
		},
		{
			name: "不是自己的工程",
			act: func(t *testing.T, f *fixture, project Project) {
				if _, err := f.service.UpdateProject(context.Background(), testOther, project.ID, "改名", ""); err == nil {
					t.Fatal("改别人的工程应当被拒")
				}
			},
		},
		{
			name: "撤回一个本来就没发布的工程",
			act: func(t *testing.T, f *fixture, project Project) {
				// 幂等，且**什么都没变**：没有变更就没有可通知的对象。
				if err := f.service.Unpublish(context.Background(), testOwner, project.ID, SlotSite); err != nil {
					t.Fatalf("撤回未发布的工程应当成功（幂等）: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			project := f.createProject(t, "工程")
			sub := f.events.Subscribe([]string{ProjectTopic(project.ID)})
			t.Cleanup(sub.Close)

			tc.act(t, f, project)

			if changed(sub) {
				t.Error("写入没有成功却发出了事件")
			}
		})
	}
}

// **主题的字面量是两个语言之间的约定**：服务端这一侧由 ProjectTopic 拼，前端
// 那一侧由 `web/src/watch/topics.ts` 拼。两边各有一条测试钉住同一个字符串（前端
// 见 `topics.test.ts`），任何一侧改了它，那一侧的测试立刻失败——否则两边会安静地
// 各说各话，而表现是"订阅了却永远收不到"。
func TestProjectTopicShape(t *testing.T) {
	if got := ProjectTopic("prj_abc"); got != "galaxy.project/prj_abc" {
		t.Errorf("ProjectTopic = %q，期望 %q", got, "galaxy.project/prj_abc")
	}
}
