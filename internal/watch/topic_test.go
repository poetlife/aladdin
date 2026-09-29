package watch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/rbac"
)

// 一个合法的注册项，供各用例复用。
func testKind() Kind {
	return Kind{
		Permission: rbac.PermissionGalaxyProjectRead,
		Authorize:  func(context.Context, rbac.Subject, string) error { return nil },
	}
}

func TestTopicIsKindSlashID(t *testing.T) {
	if got := Topic("galaxy.project", "prj_1"); got != "galaxy.project/prj_1" {
		t.Errorf("Topic = %q，期望 %q", got, "galaxy.project/prj_1")
	}
}

func TestResolveSplitsAtTheFirstSeparator(t *testing.T) {
	registry := NewRegistry()
	registry.Register("galaxy.project", testKind())

	// 资源标识里再出现斜杠不影响类型名的判定（将来的层级标识不必改这里）。
	_, id, err := registry.Resolve("galaxy.project/prj_1/附件")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if id != "prj_1/附件" {
		t.Errorf("资源标识 = %q，期望 %q", id, "prj_1/附件")
	}
}

func TestResolveRejectsUnregisteredShapes(t *testing.T) {
	registry := NewRegistry()
	registry.Register("galaxy.project", testKind())

	cases := []struct {
		name  string
		topic string
	}{
		{"没有分隔符", "galaxy.project"},
		{"类型为空", "/prj_1"},
		{"标识为空", "galaxy.project/"},
		{"空主题", ""},
		{"类型没注册过", "identity.device/dev_1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := registry.Resolve(tc.topic); !errors.Is(err, ErrUnknownTopicKind) {
				t.Errorf("Resolve(%q) 的错误 = %v，期望 ErrUnknownTopicKind", tc.topic, err)
			}
		})
	}
}

// 解析出来的判定就是注册时给的那一个：通道自己不认识任何业务。
func TestResolveReturnsTheRegisteredAuthorizer(t *testing.T) {
	registry := NewRegistry()
	var seen string
	registry.Register("galaxy.project", Kind{
		Permission: rbac.PermissionGalaxyProjectRead,
		Authorize: func(_ context.Context, _ rbac.Subject, id string) error {
			seen = id
			return errForbidden
		},
	})

	entry, id, err := registry.Resolve("galaxy.project/prj_1")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if entry.Permission != rbac.PermissionGalaxyProjectRead {
		t.Errorf("权限码 = %q，期望 %q", entry.Permission, rbac.PermissionGalaxyProjectRead)
	}
	if err := entry.Authorize(context.Background(), rbac.Subject{}, id); !errors.Is(err, errForbidden) {
		t.Errorf("判定错误 = %v，期望原样返回 errForbidden", err)
	}
	if seen != "prj_1" {
		t.Errorf("判定收到的标识 = %q，期望 %q", seen, "prj_1")
	}
}

// 装配期的错误当场 panic：它们只可能来自写错的接线，启动就炸比运行期才失败早。
func TestRegisterPanicsOnWiringMistakes(t *testing.T) {
	cases := []struct {
		name string
		once func(r *Registry)
	}{
		{"重复注册", func(r *Registry) {
			r.Register("galaxy.project", testKind())
			r.Register("galaxy.project", testKind())
		}},
		{"类型为空", func(r *Registry) { r.Register("", testKind()) }},
		{"类型含分隔符", func(r *Registry) { r.Register("galaxy/project", testKind()) }},
		{"权限码非法", func(r *Registry) {
			r.Register("galaxy.project", Kind{
				Permission: "不是权限码",
				Authorize:  func(context.Context, rbac.Subject, string) error { return nil },
			})
		}},
		{"没有归属判定", func(r *Registry) {
			r.Register("galaxy.project", Kind{Permission: rbac.PermissionGalaxyProjectRead})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					t.Fatal("没有 panic：这类接线错误必须在装配期就暴露")
				}
				if message, ok := recovered.(string); !ok || strings.TrimSpace(message) == "" {
					t.Errorf("panic 的内容 = %v，期望一句能读的说明", recovered)
				}
			}()
			tc.once(NewRegistry())
		})
	}
}

var errForbidden = errors.New("不是你的")
