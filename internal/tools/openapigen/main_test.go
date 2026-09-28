package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 这两个过程名必须是已注册的真实方法：幂等与鉴权都从描述符读，
// 描述符由 descriptors.go 的 import 注册。
const (
	readProcedure  = "/aladdin.rbac.v1.RBACService/GetRole"
	writeProcedure = "/aladdin.rbac.v1.RBACService/PutRole"
)

// docFixture 是生成器在**当前配置**下的产出形状：每条路径一个 post。
// 只读方法也一样——它额外接受的 GET 写在说明里，不单列一条 operation。
const docFixture = `openapi: 3.1.0
paths:
  ` + readProcedure + `:
    post:
      description: |-
        读取角色定义。
  ` + writeProcedure + `:
    post:
      description: |-
        创建或更新角色。
`

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.openapi.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func processFixture(t *testing.T, body string) *yaml.Node {
	t.Helper()
	path := writeFixture(t, body)
	if _, _, err := processFile(path); err != nil {
		t.Fatalf("processFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return &doc
}

func postOpFor(t *testing.T, doc *yaml.Node, procedure string) *yaml.Node {
	t.Helper()
	item := mappingValue(mappingValue(rootMapping(doc), "paths"), procedure)
	if item == nil {
		t.Fatalf("文档里没有路径 %s", procedure)
	}
	op := mappingValue(item, "post")
	if op == nil {
		t.Fatalf("路径 %s 下没有 post operation", procedure)
	}
	return op
}

// TestInjectsAuthIntoEveryMethod 固定最基本的产出：每个方法都拿到扩展与说明行。
func TestInjectsAuthIntoEveryMethod(t *testing.T) {
	doc := processFixture(t, docFixture)

	for _, procedure := range []string{readProcedure, writeProcedure} {
		op := postOpFor(t, doc, procedure)
		if mappingValue(op, extKey) == nil {
			t.Errorf("%s 缺 %s", procedure, extKey)
		}
		desc := mappingValue(op, "description")
		if desc == nil || !strings.HasPrefix(desc.Value, authLinePrefix) {
			t.Errorf("%s 的描述开头不是鉴权说明：%v", procedure, desc)
		}
	}
}

// TestIdempotentLineFollowsTheProtoOption 是本次改动的核心断言：幂等说明取自
// proto 的 idempotency_level，而不是文档里的动词。
//
// 这一条区分了两种方法——只读的 GetRole 有，写方法 PutRole 没有。若来源写错
// （例如又改回"看有没有 get operation"），在只产 post 的文档里它会整条消失，
// 那时这个断言会失败。
func TestIdempotentLineFollowsTheProtoOption(t *testing.T) {
	doc := processFixture(t, docFixture)

	readDesc := mappingValue(postOpFor(t, doc, readProcedure), "description")
	if !strings.Contains(readDesc.Value, idempotentLinePrefix) {
		t.Errorf("只读方法应有幂等说明：%q", readDesc.Value)
	}
	// GET 是服务端额外接受的形状，说明里必须写出来，否则等于把一条真实可用的
	// 调用形状藏起来。
	if !strings.Contains(readDesc.Value, "也接受 GET") {
		t.Errorf("幂等说明应写明服务端也接受 GET：%q", readDesc.Value)
	}

	writeDesc := mappingValue(postOpFor(t, doc, writeProcedure), "description")
	if strings.Contains(writeDesc.Value, idempotentLinePrefix) {
		t.Errorf("写方法不该有幂等说明：%q", writeDesc.Value)
	}
}

// TestRepeatRunIsStable 守住"先删后插"：重复运行不得把说明行越堆越多。
func TestRepeatRunIsStable(t *testing.T) {
	path := writeFixture(t, docFixture)

	if _, _, err := processFile(path); err != nil {
		t.Fatalf("首次 processFile: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := processFile(path); err != nil {
		t.Fatalf("再次 processFile: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("重复运行改变了文档：\n第一次:\n%s\n第二次:\n%s", first, second)
	}
}

// TestInjectedCountCoversEveryMethod 固定注入计数：每个方法一个。
func TestInjectedCountCoversEveryMethod(t *testing.T) {
	path := writeFixture(t, docFixture)

	_, injected, err := processFile(path)
	if err != nil {
		t.Fatalf("processFile: %v", err)
	}
	if want := 2; injected != want {
		t.Errorf("期望注入 %d 个方法，得到 %d", want, injected)
	}
}

// TestMultipleVerbsFailLoudly 守住产出契约：本工具的文档每条路径只给一个动词。
//
// 生成器一旦被配置成同时产 get 与 post（allow-get），这里必须**报错**而不是只
// 注入其中一个——只注入其一会让另一个动词静默地没有鉴权信息，而 CI 拦不住。
func TestMultipleVerbsFailLoudly(t *testing.T) {
	path := writeFixture(t, `openapi: 3.1.0
paths:
  `+readProcedure+`:
    get:
      description: |-
        读取角色定义。
    post:
      description: |-
        读取角色定义。
`)
	if _, _, err := processFile(path); err == nil {
		t.Fatal("路径下出现多个动词时必须报错")
	}
}
