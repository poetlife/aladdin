package telemetry

import (
	"testing"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
)

// TestEveryActionHasPolicy 保证 proto 里的动作枚举与策略表一一对应。
//
// 少一条（枚举新增而表没跟上）的表现是"新动作永远被丢弃"，而那与"客户端没上报"
// 在服务端看起来完全一样——所以把它提前到构建期。
func TestEveryActionHasPolicy(t *testing.T) {
	names := map[string]bool{}
	for value, name := range telemetryv1.Action_name {
		action := telemetryv1.Action(value)
		if action == telemetryv1.Action_ACTION_UNSPECIFIED {
			continue
		}
		policy, ok := actionPolicies[action]
		if !ok {
			t.Errorf("动作 %s 没有策略：新增枚举值必须在 actionPolicies 里登记", name)
			continue
		}
		if policy.name == "" {
			t.Errorf("动作 %s 缺少日志取值 name", name)
		}
		if names[policy.name] {
			t.Errorf("动作日志取值 %q 重复：聚合查询会把它当成同一个动作", policy.name)
		}
		names[policy.name] = true
	}
	if want := len(telemetryv1.Action_name) - 1; len(actionPolicies) != want {
		t.Errorf("策略表有 %d 项，枚举有 %d 个非 UNSPECIFIED 值", len(actionPolicies), want)
	}
}

// TestEveryAllowedAttrHasValidator 保证每个白名单键都有自己的取值域。
//
// sanitizeAttrs 会直接调用 validators[key]，缺一条就是一次 nil 调用（panic）。
func TestEveryAllowedAttrHasValidator(t *testing.T) {
	for action, policy := range actionPolicies {
		for _, key := range policy.attrs {
			if validators[key] == nil {
				t.Errorf("动作 %s 允许属性 %q，但 validators 里没有它的取值域", action, key)
			}
		}
	}
}

// TestEverySurfaceAndResultHaveName 保证枚举折算表覆盖全部取值。
func TestEverySurfaceAndResultHaveName(t *testing.T) {
	for value, name := range telemetryv1.Surface_name {
		surface := telemetryv1.Surface(value)
		if surface == telemetryv1.Surface_SURFACE_UNSPECIFIED {
			continue
		}
		if surfaceNames[surface] == "" {
			t.Errorf("界面 %s 没有日志取值", name)
		}
	}
	if want := len(telemetryv1.Surface_name) - 1; len(surfaceNames) != want {
		t.Errorf("surfaceNames 有 %d 项，枚举有 %d 个非 UNSPECIFIED 值", len(surfaceNames), want)
	}

	for value, name := range telemetryv1.Result_name {
		result := telemetryv1.Result(value)
		if result == telemetryv1.Result_RESULT_UNSPECIFIED {
			continue
		}
		if resultNames[result] == "" {
			t.Errorf("结局 %s 没有日志取值", name)
		}
	}
	if want := len(telemetryv1.Result_name) - 1; len(resultNames) != want {
		t.Errorf("resultNames 有 %d 项，枚举有 %d 个非 UNSPECIFIED 值", len(resultNames), want)
	}
}
