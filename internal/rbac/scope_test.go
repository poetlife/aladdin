package rbac

import "testing"

func TestScopeContains(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
		other Scope
		want  bool
	}{
		{"父子", "tenant/acme", "tenant/acme/project/web", true},
		{"自身", "tenant/acme", "tenant/acme", true},
		{"反向不成立", "tenant/acme/project", "tenant/acme", false},
		{"兄弟不成立", "tenant/acme/project", "tenant/acme/billing", false},
		{"同前缀不同层级不成立", "tenant/acme", "tenant/acmex", false},
		{"全局包含一切", GlobalScope, "tenant/acme", true},
		{"全局包含自身", GlobalScope, GlobalScope, true},
		{"非全局不包含全局", "tenant/acme", GlobalScope, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.Contains(tt.other); got != tt.want {
				t.Errorf("%q.Contains(%q) = %v, want %v", tt.scope, tt.other, got, tt.want)
			}
		})
	}
}

func TestScopeString(t *testing.T) {
	if got := GlobalScope.String(); got != "<global>" {
		t.Errorf("全局作用域应显示为 <global>，实际 %q", got)
	}
	if got := Scope("a/b").String(); got != "a/b" {
		t.Errorf("普通作用域应原样显示，实际 %q", got)
	}
}

func TestParseScope(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Scope
	}{
		{"全局哨兵解析为全局", GlobalScopeLiteral, GlobalScope},
		{"普通作用域原样使用", "tenant/acme", Scope("tenant/acme")},
		{"不做任何规范化", "tenant/acme/", Scope("tenant/acme/")},
		{"哨兵区分大小写", "<Global>", Scope("<Global>")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseScope(tt.text); got != tt.want {
				t.Errorf("ParseScope(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// String 与 ParseScope 必须互逆。
//
// 这不是形式上的对称：运维的实际动作就是**照日志里的作用域改配置**，
// 而日志里打印的是 String() 的结果。不互逆意味着"照抄一遍"会静默改掉
// 授权范围——全局被抄成字面量、或字面量被抄成别的什么。
func TestScopeTextRoundTrip(t *testing.T) {
	for _, s := range []Scope{GlobalScope, "tenant/acme", "tenant/acme/project"} {
		if got := ParseScope(s.String()); got != s {
			t.Errorf("ParseScope(%q.String()) = %q，期望 %q", s, got, s)
		}
	}
}
