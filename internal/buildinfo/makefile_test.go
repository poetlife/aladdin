package buildinfo_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// injectPattern 匹配 Makefile 里的一处链接器注入：`-X <包路径>.<变量名>=...`。
//
// 值里可能出现 `$(` 形式的 make 变量，因此这里不锚定行尾，只取到 `=` 之前的
// 符号名——本测试关心的正是符号名对不对。
var injectPattern = regexp.MustCompile(`-X\s+([^\s=]+)\.([A-Za-z_][A-Za-z0-9_]*)=`)

// assignPattern 匹配 Makefile 里的一行简单赋值（`NAME := 值` / `NAME ?= 值`）。
//
// 只看单行：本文件里 LDFLAGS 的值跨在续行上，但那不影响什么——展开是整个文件
// 一起做的，续行上的 `-X` 照样会被展开到。
var assignPattern = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)\s*[:?]?=\s*(.+)$`)

// makeVarPattern 匹配一处 `$(NAME)` 引用。
var makeVarPattern = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)

// TestMakefileInjectsOnlyDeclaredVariables 校验 Makefile 的 -X 注入点指向本包里
// 真实存在的变量。
//
// **链接器 -X 在符号名写错时是静默失效的**：变量保持默认值，产物悄悄退化成
// "dev"，而构建、测试、发布一路绿灯。这类错误没有任何运行时症状，只在有人认真
// 看版本号那天才被发现——而那天通常是排障现场。
//
// 判据取自仓库里的源文件本身（Makefile 与 go.mod），不在这里抄一份符号名：
// 抄一份等于把"唯一信源"换成两个会漂的列表，而漂移的表现正是本测试想防的那件事。
func TestMakefileInjectsOnlyDeclaredVariables(t *testing.T) {
	root := filepath.Join("..", "..")

	module := modulePath(t, root)
	pkgPath := module + "/internal/buildinfo"

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("读 Makefile 失败: %v", err)
	}

	// Makefile 把包路径写成了一个变量（`-X $(BUILDINFO).Version=...`），于是先把它
	// 自己的变量展开一遍，否则这里比对的会是字面量 `$(BUILDINFO)`。展开只认本文件
	// 里的简单赋值，`$(shell ...)` 这类函数原样留着——它们不参与包路径的拼装。
	text := expandMakeVars(string(makefile))
	if !strings.Contains(text, pkgPath) {
		t.Fatalf("展开后仍找不到包路径 %s，变量展开可能没生效", pkgPath)
	}

	declared := declaredVars(t, ".")

	injected := map[string]bool{}
	for _, m := range injectPattern.FindAllStringSubmatch(text, -1) {
		if m[1] != pkgPath {
			continue
		}
		name := m[2]
		injected[name] = true
		if !declared[name] {
			t.Errorf("Makefile 向 %s.%s 注入，但本包里没有这个包级变量——"+
				"链接器的 -X 在符号名写错时静默失效，构建期不会报错", pkgPath, name)
		}
	}

	// 反向也要判：一个声明了却没人注入的变量，读出来永远是默认值，而那与
	// "注入失败"在页面上长得一模一样。
	for _, name := range []string{"Version", "Commit", "BuildTime", "Released"} {
		if !injected[name] {
			t.Errorf("Makefile 没有向 %s 注入：Makefile 的 LDFLAGS 是它的唯一注入点", name)
		}
	}
}

// expandMakeVars 反复展开文本里的 `$(NAME)`，值取自文本自身的简单赋值。
//
// 反复是因为可以套几层（BUILDINFO 用 MODULE 拼出来）。轮数有上界：一个自引用的
// 赋值会让展开停不下来，而那是 Makefile 的问题，不该让本测试挂死。
const makeVarRounds = 8

func expandMakeVars(text string) string {
	defs := map[string]string{}
	for _, m := range assignPattern.FindAllStringSubmatch(text, -1) {
		defs[m[1]] = m[2]
	}

	for range makeVarRounds {
		expanded := makeVarPattern.ReplaceAllStringFunc(text, func(ref string) string {
			name := makeVarPattern.FindStringSubmatch(ref)[1]
			if value, ok := defs[name]; ok {
				return value
			}
			// 认不出的引用原样留着（`$(shell ...)`、`$(if ...)` 等都走这里）：
			// 它们不参与包路径的拼装，猜一个展开结果只会引入假结论。
			return ref
		})
		if expanded == text {
			break
		}
		text = expanded
	}
	return text
}

// modulePath 从 go.mod 读模块路径，供上面的用例拼出本包的完整导入路径。
func modulePath(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("读 go.mod 失败: %v", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("go.mod 里没有 module 行")
	return ""
}

// declaredVars 收集目录下**非测试** Go 文件里的包级变量名。
//
// 只看包级：-X 注入的必须是包级变量，函数内的局部变量注入不进去。
//
// 逐个文件解析而不是用 parser.ParseDir：后者按目录把所有 .go 归成一个包，不看
// 构建约束——本测试关心的只是"这个名字有没有声明"，逐文件解析的判据更直白。
func declaredVars(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读包目录失败: %v", err)
	}

	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, ident := range value.Names {
					out[ident.Name] = true
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("未解析到任何包级变量：解析范围可能不对")
	}
	return out
}
