package server

import (
	"testing"

	"connectrpc.com/connect"

	profilev1 "github.com/poetlife/aladdin/api/gen/aladdin/profile/v1"
	"github.com/poetlife/aladdin/internal/profile"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 档案接口面上**不存在以他人为目标的形状**。
//
// 这是"每个方法都只作用于调用者自己"这条约束的构建期落点，也是它敢不要
// 权限码的全部依据：没有目标字段，就没有可被滥用的授权面
// （见 docs/design/profile/README.md）。人工 review 会漏，所以在这里挡一道。
//
// 它扫的是生成出来的 method descriptor，因此新加一个带 `subject_id` 的档案
// 方法会**直接让构建失败**，而不是等到有人读 proto 时才发现。
func TestProfileInterfaceHasNoOtherSubjectTarget(t *testing.T) {
	// 这些名字都表示"由请求指定一个目标主体"。出现任何一个，就意味着接口面
	// 上多了一个以他人为目标的形状。
	forbidden := map[string]bool{
		"subject_id": true,
		"subject":    true,
		"target":     true,
	}

	services := profilev1.File_aladdin_profile_v1_profile_proto.Services()
	if services.Len() != 1 {
		t.Fatalf("档案 proto 里有 %d 个服务，期望 1 个", services.Len())
	}

	methods := services.Get(0).Methods()
	if methods.Len() == 0 {
		t.Fatal("档案服务没有任何方法")
	}
	for i := 0; i < methods.Len(); i++ {
		method := methods.Get(i)
		fields := method.Input().Fields()
		for j := 0; j < fields.Len(); j++ {
			name := string(fields.Get(j).Name())
			if forbidden[name] {
				t.Errorf("%s 的请求里有字段 %q：档案方法不得以指定主体为目标", method.Name(), name)
			}
		}
	}
}

// 错误映射按"调用方该做什么"分类，而不是按错误来自哪一层。
//
// 分错类的代价是具体的：把长度超限报成 Unavailable 会让客户端无脑重试，
// 把存储故障报成 InvalidArgument 会让它改一个本来没错的输入。
func TestProfileErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want connect.Code
	}{
		{"昵称过长", profile.ErrNicknameTooLong, connect.CodeInvalidArgument},
		{"简介过长", profile.ErrBioTooLong, connect.CodeInvalidArgument},
		{"头像类型不符", profile.ErrAvatarTypeNotAllowed, connect.CodeInvalidArgument},
		{"头像超限", profile.ErrAvatarTooLarge, connect.CodeInvalidArgument},
		{"未启用头像", profile.ErrAvatarUnavailable, connect.CodeFailedPrecondition},
		{"存储不可用", profile.ErrStoreUnavailable, connect.CodeUnavailable},
		{"主体不存在", rbac.ErrSubjectNotFound, connect.CodeNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := connect.CodeOf(toProfileConnectError(tc.err)); got != tc.want {
				t.Errorf("错误码 = %v，期望 %v", got, tc.want)
			}
		})
	}
}
