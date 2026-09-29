package server

import (
	"fmt"
	"math"
	"time"

	"connectrpc.com/connect"

	objectstorev1 "github.com/poetlife/aladdin/api/gen/aladdin/objectstore/v1"
	"github.com/poetlife/aladdin/internal/objectstore"
)

// declaredBytes 把一个**声明**的字节数转成领域层的取值。
//
// 声明是不可信输入：超过 int64 上界的取值转过来会回绕成负数，而负数会通过
// "有没有超过上限"这个判断。因此这里显式拒绝，而不是让它静默回绕成一个能过
// 校验的值。
func declaredBytes(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("声明的大小 %d 超出可表示范围", value))
	}
	return int64(value), nil //nolint:gosec // 上界已在上面显式判定
}

// fitUint32 把一个领域层的字节数转成接口类型。
//
// 上限都是常量（最大 100 MiB），远在 uint32 之内；取**饱和**而不是断言，是为了
// 让"有人把上限调到 4 GiB 以上"在接口上表现为一个明确的上界，而不是一个回绕成
// 小数字的值——后者会让客户端以为上限比实际小得多。
func fitUint32(value int64) uint32 {
	switch {
	case value < 0:
		return 0
	case value > math.MaxUint32:
		return math.MaxUint32
	default:
		return uint32(value) //nolint:gosec // 上下界已在上面显式判定
	}
}

// fitUint64 把一个领域层的字节数转成无符号的接口类型。
func fitUint64(value int64) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value)
}

// fitInt32 把一个领域层的计数值转成接口类型。
//
// 取**饱和**而不是断言：行号与渲染规则版本都远在 int32 之内，而一个越界的取值
// 应当表现为一个明确的上界，不是一个回绕成负数的数。
func fitInt32(value int) int32 {
	switch {
	case value < math.MinInt32:
		return math.MinInt32
	case value > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(value) //nolint:gosec // 上下界已在上面显式判定
	}
}

// toProtoUpload 把一份直传凭证翻译成接口类型（唯一入口）。
//
// 头像与资产两处都下发同一个消息（见
// api/proto/aladdin/objectstore/v1/upload.proto），因此转换也只有一处：两处各写
// 一份的表现是"换一个功能上传，少了一个字段"，而它要到客户端直传被拒才发现。
//
// 凭证里的密钥是**临时**的、只对一个键有效、短时过期——这正是它可以随响应下发
// 的原因。但它同样**不得进日志**。
func toProtoUpload(credential objectstore.Credential) *objectstorev1.DirectUploadCredential {
	return &objectstorev1.DirectUploadCredential{
		Bucket:          credential.Bucket,
		Region:          credential.Region,
		Key:             credential.Key,
		SecretId:        credential.SecretID,
		SecretKey:       credential.SecretKey,
		SessionToken:    credential.SessionToken,
		ExpiresAt:       credential.ExpiresAt.UTC().Format(time.RFC3339),
		ForbidOverwrite: credential.ForbidOverwrite,
	}
}
