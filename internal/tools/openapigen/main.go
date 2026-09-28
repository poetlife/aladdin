// Command openapigen 给生成的 OpenAPI 文档补上鉴权信息，并产出单份合并文档。
//
// 用法（在仓库根目录执行）：
//
//	make api-docs          # 生成 + 注入 + 合并，一步到位
//	go run ./internal/tools/openapigen -dir api/openapi -merge-out web/public/api-docs/openapi.yaml
//
// 输入：api/openapi 下由 protoc-gen-connect-openapi 产出的 *.openapi.yaml
// 输出：原地更新，为每个 RPC 方法的每个 HTTP operation 挂上 x-aladdin-auth 扩展
// 输出：按 -merge-out 产出一份合并文档（渲染器一次只吃一份 spec）
//
// 为什么需要这一步：OpenAPI 生成器只认识公开的注解族（gnostic、
// protovalidate、google.api.http），而 aladdin 的鉴权声明是自己的扩展
// （api/proto/aladdin/rbac/v1/annotations.proto），生成器看不见它。
// 结果是文档里 public 与 authenticated_only 的方法长得一模一样——
// 而这恰恰是调用方最先要知道的信息。
//
// 这里**不重新解析注解**：OpenAPI 的 path 就是 RPC 过程名（见
// internal/server/connect.go 的路径约定），直接喂给 rbac.Resolve 即可，
// 判定语义与拦截器同源，不可能漂移。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"

	"github.com/poetlife/aladdin/internal/rbac"
)

// httpMethods 是 path item 下可能出现的 HTTP 方法键。
// 生成器默认只产 post，本仓库的文档即以此为产出契约。
var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true,
	"delete": true, "patch": true, "head": true, "options": true,
}

func main() {
	dir := flag.String("dir", "api/openapi", "OpenAPI 文档目录（原地更新）")
	mergeOut := flag.String("merge-out", "", "合并后的单份文档路径（为空则不产出）")
	flag.Parse()

	files, err := findDocs(*dir)
	if err != nil {
		fail(err)
	}
	if len(files) == 0 {
		fail(fmt.Errorf("%s 下没有找到 *.openapi.yaml——先跑 buf generate 产出原始文档", *dir))
	}

	injected := 0
	docs := make([]*yaml.Node, 0, len(files))
	for _, path := range files {
		doc, n, err := processFile(path)
		if err != nil {
			fail(err)
		}
		injected += n
		docs = append(docs, doc)
	}

	if *mergeOut != "" {
		merged, err := mergeDocs(docs)
		if err != nil {
			fail(err)
		}
		if err := writeDoc(*mergeOut, merged); err != nil {
			fail(err)
		}
	}

	fmt.Printf("openapigen: %d 个文件、%d 个方法已注入 %s\n", len(files), injected, extKey)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "openapigen: %v\n", err)
	os.Exit(1)
}

// findDocs 递归收集目录下的 OpenAPI 文档。
func findDocs(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".openapi.yaml") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// processFile 就地把鉴权扩展写进一份文档，返回该文档的节点与注入的方法数。
func processFile(path string) (*yaml.Node, int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // 路径来自本工具自身的 -dir 参数
	if err != nil {
		return nil, 0, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, 0, fmt.Errorf("%s: 解析 YAML 失败: %w", path, err)
	}

	paths := mappingValue(rootMapping(&doc), "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		// 没有 paths（例如纯 schema 文件）不是错误，只是没什么可注入的。
		// 仍要返回节点：它的 components 要参与合并。
		return &doc, 0, nil
	}

	injected := 0
	for i := 0; i+1 < len(paths.Content); i += 2 {
		procedure := paths.Content[i].Value
		pathItem := paths.Content[i+1]

		rule, err := rbac.Resolve(procedure)
		if err != nil {
			// 解析失败说明文档里出现了非 RPC 路径，或描述符未注册——
			// 两种都是真问题，不静默跳过。
			return nil, 0, fmt.Errorf("%s: 方法 %s 解析注解失败: %w", path, procedure, err)
		}

		op, err := operationNode(pathItem)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: 方法 %s: %w", path, procedure, err)
		}
		if op == nil {
			continue
		}

		// 幂等这一事实取自 proto 的 idempotency_level，与鉴权注解同源（同一份方法
		// 描述符）。文档只给 POST 一种形状，但该方法在服务端**确实也接受 GET**，
		// 所以这句话要写出来：少写等于把一条真实可用的调用形状藏起来。
		idempotent, err := noSideEffects(procedure)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: 方法 %s: %w", path, procedure, err)
		}

		setAuth(op, rule, idempotent)
		injected++
	}

	if err := writeDoc(path, &doc); err != nil {
		return nil, 0, err
	}
	return &doc, injected, nil
}

// rootMapping 取文档根节点对应的 MappingNode。
func rootMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

// mappingValue 在 MappingNode 中按键取值，不存在时返回 nil。
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// operationNode 取 path item 下的 HTTP operation 节点。
//
// 本工具的产出契约是**每条路径恰好一个 operation**：文档只给 POST 一种形状，
// 只读方法也一样——GET 是服务端额外接受的动词，写在方法的说明里，不单列一条。
// 出现多个动词时直接报错而不是只取其一：只取其一会让另一个动词静默地没有鉴权
// 信息，而 CI 拦不住。真要并列展示，先扩展本工具使其逐个注入。
func operationNode(pathItem *yaml.Node) (*yaml.Node, error) {
	if pathItem == nil || pathItem.Kind != yaml.MappingNode {
		return nil, nil
	}
	var (
		node *yaml.Node
		verb string
	)
	for i := 0; i+1 < len(pathItem.Content); i += 2 {
		v := pathItem.Content[i].Value
		if !httpMethods[v] {
			continue
		}
		if node != nil {
			return nil, fmt.Errorf("路径下有多个 HTTP operation（%s 与 %s）", verb, v)
		}
		node, verb = pathItem.Content[i+1], v
	}
	return node, nil
}

// noSideEffects 报告方法是否声明为无副作用（idempotency_level = NO_SIDE_EFFECTS）。
//
// 事实来源是 proto：走与鉴权注解同一份方法描述符。本模块只做呈现，不下判断，
// 也不自己维护一份"哪些方法只读"的清单——那种清单必然与 proto 漂移。
func noSideEffects(procedure string) (bool, error) {
	desc, err := rbac.MethodDescriptor(procedure)
	if err != nil {
		return false, err
	}
	opts, ok := desc.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return false, nil
	}
	return opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS, nil
}

// removeKey 从 MappingNode 中摘除一个键。
func removeKey(node *yaml.Node, key string) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

func appendKV(node *yaml.Node, key, value string) {
	node.Content = append(node.Content, scalar(key), scalar(value))
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// writeDoc 写出文档，沿用生成器的 2 空格缩进。
func writeDoc(path string, doc *yaml.Node) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("%s: 创建目录失败: %w", path, err)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("%s: 序列化失败: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: 序列化失败: %w", path, err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644) //nolint:gosec // 生成的是文档，按常规权限落盘
}
