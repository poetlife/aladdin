package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 鉴权信息在文档里有两种呈现，同一个规则一并产出：
//
//   - x-aladdin-auth —— 给机器读的结构化扩展
//   - description 开头的一行 —— 给人读的可读说明
//
// 两种都要，因为标准渲染器（Redoc、Swagger UI）只渲染 description：
// 自定义扩展要么根本不显示，要么以原始 JSON 显示——对读文档的人来说，
// {"kind":"requires","permission":"rbac.role.read"} 等于没说。

const extKey = "x-aladdin-auth"

// authLinePrefix 标记注入到 description 的那一行，用于重复运行时识别并替换。
const authLinePrefix = "**鉴权**："

// idempotentLinePrefix 同上，标记只读方法的幂等说明行。
const idempotentLinePrefix = "**幂等**："

// idempotentText 是只读方法的一句话说明。
//
// 为什么必须写出来：这条路径在服务端**同时接受 GET 与 POST**，而文档只给一种
// 形状（POST，与其余方法一致）。不写这句，"GET 也能调"就成了一条被藏起来的
// 事实；写了，读者不必自己知道 Connect 里"GET ⇒ 无副作用"这条映射。
const idempotentText = "本方法无副作用，可安全重试；服务端在同一路径上也接受 GET。"

// setAuth 把鉴权规则与幂等说明写进一个 operation。
//
// idempotent 为真时追加幂等说明行。
//
// 两种呈现都是先删后插，因此对同一份文档重复执行结果一致。
func setAuth(op *yaml.Node, rule rbac.MethodRule, idempotent bool) {
	removeKey(op, extKey)
	op.Content = append([]*yaml.Node{scalar(extKey), authExt(rule)}, op.Content...)
	setAuthLine(op, rule, idempotent)
}

// authExt 构造扩展的取值节点。
//
// 只写出**真正参与判定**的字段：public 免鉴权，authenticated_only 不看作用域
// （拦截器在权限检查之前就放行了），两者带上 scope-source 只会让读者误以为
// 它有约束力。requires 则相反——未声明作用域来源时鉴权以"作用域不符"拒绝，
// 所以哪怕取值是 UNSPECIFIED 也必须如实写出。
func authExt(rule rbac.MethodRule) *yaml.Node {
	fields := &yaml.Node{Kind: yaml.MappingNode}
	appendKV(fields, "kind", rule.Kind.String())
	if rule.Kind == rbac.KindRequires {
		appendKV(fields, "permission", string(rule.Permission))
		appendKV(fields, "scope-source", rule.ScopeFrom.String())
	}
	return fields
}

// setAuthLine 把可读的说明放在 description 开头。
//
// 放开头是因为它是调用方最先要知道的信息；proto 里的原始注释跟在后面。
func setAuthLine(op *yaml.Node, rule rbac.MethodRule, idempotent bool) {
	line := authLinePrefix + authText(rule)
	if idempotent {
		line += "\n\n" + idempotentLinePrefix + idempotentText
	}

	desc := mappingValue(op, "description")
	if desc == nil || desc.Kind != yaml.ScalarNode {
		op.Content = append(op.Content, scalar("description"), scalar(line))
		return
	}

	body := stripInjectedLines(desc.Value)
	if body == "" {
		desc.Value = line
		return
	}
	desc.Value = line + "\n\n" + body
}

// stripInjectedLines 剥掉上一次注入的说明行，返回 proto 原始注释。
//
// 不剥的话，重复运行会在描述里把说明行越堆越多。注入块是开头连续的若干段
// （每段以空行分隔），因此按前缀逐段剥即可。
func stripInjectedLines(body string) string {
	for strings.HasPrefix(body, authLinePrefix) || strings.HasPrefix(body, idempotentLinePrefix) {
		i := strings.Index(body, "\n\n")
		if i < 0 {
			return ""
		}
		body = body[i+2:]
	}
	return body
}

// authText 是鉴权要求的一句话说明。
func authText(rule rbac.MethodRule) string {
	switch rule.Kind {
	case rbac.KindPublic:
		return "公开接口，无需认证。"
	case rbac.KindAuthenticatedOnly:
		return "需要登录。任何已认证主体均可调用。"
	case rbac.KindRequires:
		return fmt.Sprintf("需要权限 `%s`；%s。", rule.Permission, scopeText(rule.ScopeFrom))
	default:
		return "**未声明鉴权注解**，服务端会拒绝调用。"
	}
}

// scopeText 把作用域来源翻成一句话。
//
// 与 rbac.Kind.String() 不同：那个是对外契约标识符（写进 x- 扩展），
// 这个是给人看的文案，两者可以各自演进。
func scopeText(s rbacv1.ScopeSource) string {
	switch s {
	case rbacv1.ScopeSource_SCOPE_SOURCE_REQUEST_FIELD:
		return "作用域取自请求字段"
	case rbacv1.ScopeSource_SCOPE_SOURCE_METADATA:
		return "作用域取自请求元数据 aladdin-scope"
	case rbacv1.ScopeSource_SCOPE_SOURCE_CREDENTIAL:
		return "作用域取自凭证的默认作用域"
	default:
		return "未声明作用域来源，服务端会以「作用域不符」拒绝"
	}
}
