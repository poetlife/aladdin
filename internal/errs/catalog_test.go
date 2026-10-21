package errs

import (
	"os/exec"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
)

// TestCatalogCoversEveryDeclaredReason 校验生成目录与 proto 描述符一一对应。
//
// 它不依赖 go run：即使有人绕过 make gen 手改了生成文件，这里也会直接失败。
func TestCatalogCoversEveryDeclaredReason(t *testing.T) {
	enum := errorsv1.File_aladdin_errors_v1_errors_proto.Enums().ByName("FailureReason")
	if enum == nil {
		t.Fatal("proto 里没有 FailureReason 枚举")
	}

	declared := map[errorsv1.FailureReason]bool{}
	for i := 0; i < enum.Values().Len(); i++ {
		value := enum.Values().Get(i)
		reason := errorsv1.FailureReason(int32(value.Number()))

		if value.Number() == 0 {
			if _, ok := failureValues[reason]; ok {
				t.Error("FAILURE_REASON_UNSPECIFIED 不应出现在失败目录里")
			}
			continue
		}
		declared[reason] = true

		generated, ok := failureValues[reason]
		if !ok {
			t.Errorf("失败原因 %q 没有生成对应条目", value.Name())
			continue
		}
		if generated.text == "" {
			t.Errorf("失败原因 %q 的默认文案为空", value.Name())
		}
		if generated.code < connect.CodeCanceled || generated.code > connect.CodeUnauthenticated {
			t.Errorf("失败原因 %q 的传输码 %v 不在标准十六码内", value.Name(), generated.code)
		}

		// 生成物必须与 proto 上的选项一致：这是"改 proto 就够"的实际含义。
		options, _ := value.Options().(*descriptorpb.EnumValueOptions)
		if want := stringOption(options, errorsv1.E_Text); generated.text != want {
			t.Errorf("失败原因 %q 的文案 = %q, proto 里是 %q", value.Name(), generated.text, want)
		}
		wantCode, ok := connectCodeByName(stringOption(options, errorsv1.E_ConnectCode))
		if !ok {
			t.Errorf("失败原因 %q 的 connect_code 不合法", value.Name())
			continue
		}
		if generated.code != wantCode {
			t.Errorf("失败原因 %q 的传输码 = %v, proto 里是 %v", value.Name(), generated.code, wantCode)
		}
	}

	for reason := range failureValues {
		if !declared[reason] {
			t.Errorf("生成目录里有 proto 未声明的失败原因 %v", reason)
		}
	}
}

// TestGeneratedFailuresInSync 校验生成产物与 proto 同步，且编号、分段、
// 传输码都通过生成器的构建期检查。
func TestGeneratedFailuresInSync(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过：需要执行 go run")
	}
	cmd := exec.Command("go", "run", "./internal/tools/failuregen", "-check", "-root", ".")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("生成产物与 errors.proto 不同步，请运行 make gen\n%s", out)
	}
}

func stringOption(options *descriptorpb.EnumValueOptions, extension protoreflect.ExtensionType) string {
	if options == nil || !proto.HasExtension(options, extension) {
		return ""
	}
	value, _ := proto.GetExtension(options, extension).(string)
	return value
}

// connectCodeByName 用标准码自己的 String() 反查，不另抄一张映射表。
func connectCodeByName(name string) (connect.Code, bool) {
	for code := connect.CodeCanceled; code <= connect.CodeUnauthenticated; code++ {
		if code.String() == name {
			return code, true
		}
	}
	return 0, false
}
