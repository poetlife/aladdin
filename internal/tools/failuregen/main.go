// Command failuregen 从 proto 里的失败目录生成 Go 的错误值。
//
// 用法（在仓库根目录执行）：
//
//	go run ./internal/tools/failuregen
//
// 输入：api/proto/aladdin/errors/v1/errors.proto（失败原因的唯一信源）
// 输出：internal/errs/failures_gen.go
//
// 它只生成 Go 侧：TypeScript 侧需要的是枚举取值本身，由 buf generate 直接
// 派生；文案与传输码随错误详情下发，客户端不需要在本地再抄一份。
//
// 生成器同时是构建期门禁：缺文案、缺传输码、码的拼写不认识、编号越出
// 所属分段、生成的标识符重名，都会让 make gen（或 -check）直接失败。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
)

// segment 是一段预留的编号区间。
//
// 它与 docs/design/errors/README.md 的分段表一一对应：新增领域先改文档、
// 再改这里，最后才在 proto 里使用。让文档与代码同时漂移的唯一办法是
// 只把分段写成注释，因此这里必须是可执行的检查。
type segment struct {
	min  int32
	max  int32
	name string
}

var segments = []segment{
	{1000, 1099, "跨领域通用失败"},
	{1100, 1199, "认证与鉴权"},
	{1200, 1299, "身份与个人档案"},
	{1300, 1399, "平台技能目录"},
	{1400, 1499, "galaxy 创作与发布"},
	{1500, 1599, "注册与邀请"},
	{1900, 1999, "服务端内部失败"},
}

// parseConnectCode 把 connect_code 选项里的字符串解析为标准传输码。
//
// 只认十六个标准码：失败原因是在这些码之上的细分，不是第二套码表。
// 拼写错误的码在这里直接失败，而不是等到运行时被解析成 Unknown。
// 用 Code.String() 做匹配，避免再手抄一张字符串到常量的表。
func parseConnectCode(name string) (connect.Code, bool) {
	for code := connect.CodeCanceled; code <= connect.CodeUnauthenticated; code++ {
		if code.String() == name {
			return code, true
		}
	}
	return 0, false
}

// reason 是一个待生成的失败原因。
type reason struct {
	number    int32
	protoName string
	goName    string
	code      connect.Code
	text      string
}

func main() {
	root := flag.String("root", ".", "仓库根目录")
	check := flag.Bool("check", false, "只校验生成产物是否与 proto 同步，不写文件")
	flag.Parse()

	if err := run(*root, *check); err != nil {
		fmt.Fprintf(os.Stderr, "failuregen: %v\n", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	reasons, err := load()
	if err != nil {
		return err
	}

	source, err := format.Source([]byte(render(reasons)))
	if err != nil {
		return fmt.Errorf("生成的 Go 源码无法格式化: %w", err)
	}

	path := filepath.Join(root, "internal/errs/failures_gen.go")
	if check {
		existing, err := os.ReadFile(path) //nolint:gosec // 路径来自本工具自身的固定输出
		if err != nil || !bytes.Equal(existing, source) {
			return fmt.Errorf("%s 与 api/proto/aladdin/errors/v1/errors.proto 不同步，请运行 make gen", path)
		}
		return nil
	}
	if err := os.WriteFile(path, source, 0o644); err != nil { //nolint:gosec // 生成的是源码，按常规权限落盘
		return err
	}
	fmt.Printf("生成 %s\n", path)
	return nil
}

// load 读取 proto 描述符里的失败目录并校验它。
func load() ([]reason, error) {
	file := errorsv1.File_aladdin_errors_v1_errors_proto
	enum := file.Enums().ByName("FailureReason")
	if enum == nil {
		return nil, fmt.Errorf("%s 里没有 FailureReason 枚举", file.Path())
	}

	var reasons []reason
	seenNumber := map[int32]string{}
	seenGoName := map[string]string{}
	seenProtoName := map[string]bool{}

	for i := 0; i < enum.Values().Len(); i++ {
		value := enum.Values().Get(i)
		if seenProtoName[string(value.Name())] {
			return nil, fmt.Errorf("失败原因 %q 重复", value.Name())
		}
		seenProtoName[string(value.Name())] = true

		options, _ := value.Options().(*descriptorpb.EnumValueOptions)
		text := optionString(options, errorsv1.E_Text)
		codeName := optionString(options, errorsv1.E_ConnectCode)

		if value.Number() == 0 {
			if value.Name() != "FAILURE_REASON_UNSPECIFIED" {
				return nil, fmt.Errorf("零值必须是 FAILURE_REASON_UNSPECIFIED，实际是 %q", value.Name())
			}
			if text != "" || codeName != "" {
				return nil, fmt.Errorf("零值 FAILURE_REASON_UNSPECIFIED 不得声明文案或传输码")
			}
			continue
		}

		if !strings.HasPrefix(string(value.Name()), "FAILURE_REASON_") {
			return nil, fmt.Errorf("失败原因 %q 必须以 FAILURE_REASON_ 开头", value.Name())
		}
		if text == "" {
			return nil, fmt.Errorf("失败原因 %q 缺少 text", value.Name())
		}
		if strings.TrimSpace(text) != text {
			return nil, fmt.Errorf("失败原因 %q 的 text 首尾有空白", value.Name())
		}
		code, ok := parseConnectCode(codeName)
		if !ok {
			return nil, fmt.Errorf("失败原因 %q 的 connect_code %q 不是标准码", value.Name(), codeName)
		}
		number := int32(value.Number())
		if err := checkSegment(number, value.Name()); err != nil {
			return nil, err
		}
		if prev, ok := seenNumber[number]; ok {
			return nil, fmt.Errorf("失败原因 %q 与 %q 编号相同（%d）", value.Name(), prev, number)
		}
		seenNumber[number] = string(value.Name())

		goName := goIdentifier(string(value.Name()))
		if prev, ok := seenGoName[goName]; ok {
			return nil, fmt.Errorf("失败原因 %q 与 %q 生成的标识符相同（%s），请调整命名", value.Name(), prev, goName)
		}
		seenGoName[goName] = string(value.Name())

		reasons = append(reasons, reason{
			number:    number,
			protoName: string(value.Name()),
			goName:    goName,
			code:      code,
			text:      text,
		})
	}

	if len(reasons) == 0 {
		return nil, fmt.Errorf("FailureReason 里没有任何非零取值")
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i].number < reasons[j].number })
	return reasons, nil
}

// optionString 读取枚举值上的字符串选项；未设置时返回空串。
func optionString(options *descriptorpb.EnumValueOptions, extension protoreflect.ExtensionType) string {
	if options == nil || !proto.HasExtension(options, extension) {
		return ""
	}
	value, _ := proto.GetExtension(options, extension).(string)
	return value
}

func checkSegment(number int32, name protoreflect.Name) error {
	for _, s := range segments {
		if number >= s.min && number <= s.max {
			return nil
		}
	}
	return fmt.Errorf("失败原因 %q 的编号 %d 不在任何已登记的分段里（见 docs/design/errors/README.md）", name, number)
}

// goIdentifier 由 proto 枚举值名生成 Go 标识符：
// FAILURE_REASON_STORE_UNAVAILABLE -> ErrStoreUnavailable。
func goIdentifier(protoName string) string {
	trimmed := strings.TrimPrefix(protoName, "FAILURE_REASON_")
	var b strings.Builder
	b.WriteString("Err")
	for _, word := range strings.Split(trimmed, "_") {
		if word == "" {
			continue
		}
		runes := []rune(strings.ToLower(word))
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	return b.String()
}

func render(reasons []reason) string {
	var b strings.Builder
	b.WriteString("// Code generated by internal/tools/failuregen. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// 唯一信源：api/proto/aladdin/errors/v1/errors.proto\n")
	b.WriteString("// 重新生成：make gen\n\n")
	b.WriteString("package errs\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"connectrpc.com/connect\"\n\n")
	b.WriteString("\terrorsv1 \"github.com/poetlife/aladdin/api/gen/aladdin/errors/v1\"\n")
	b.WriteString(")\n\n")

	b.WriteString("// failureValues 是失败原因到传输码与默认文案的目录。\n")
	b.WriteString("//\n")
	b.WriteString("// 它由 proto 枚举值上的选项生成：同一个原因在所有方法里只有一个码，\n")
	b.WriteString("// 调用方不能单独指定码。\n")
	b.WriteString("var failureValues = map[errorsv1.FailureReason]failureValue{\n")
	for _, r := range reasons {
		fmt.Fprintf(&b, "\terrorsv1.FailureReason_%s: {\n", r.protoName)
		fmt.Fprintf(&b, "\t\tcode: %s,\n", connectCodeLiteral(r.code))
		fmt.Fprintf(&b, "\t\ttext: %q,\n", r.text)
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n\n")

	b.WriteString("// 以失败原因命名的错误值。\n")
	b.WriteString("//\n")
	b.WriteString("// 领域代码直接返回它们，或包上因果链（见 Wrap）。共享值不得原地修改：\n")
	b.WriteString("// 需要补充上下文时用 WithMetadata / Newf，它们返回副本。\n")
	b.WriteString("var (\n")
	for _, r := range reasons {
		fmt.Fprintf(&b, "\t%s = New(errorsv1.FailureReason_%s)\n", r.goName, r.protoName)
	}
	b.WriteString(")\n")
	return b.String()
}

// connectCodeLiteral 把传输码还原成源码里的常量名。
//
// connect.Code 的 String 与其常量一一对应（CodeNotFound <-> "not_found"），
// 因此不需要再维护一张反向表。
func connectCodeLiteral(code connect.Code) string {
	return "connect." + codeName(code.String())
}

// codeName 把 "not_found" 转成 connect.Code 常量名里的 "NotFound"。
func codeName(snake string) string {
	var b strings.Builder
	b.WriteString("Code")
	for _, word := range strings.Split(snake, "_") {
		if word == "" {
			continue
		}
		runes := []rune(word)
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	return b.String()
}
