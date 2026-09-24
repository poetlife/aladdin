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
