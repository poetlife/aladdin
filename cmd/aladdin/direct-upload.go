package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/tencentyun/cos-go-sdk-v5"

	objectstorev1 "github.com/poetlife/aladdin/api/gen/aladdin/objectstore/v1"
)

// directUploadTimeout 是一次直传的超时。
//
// 它**不是 --timeout**：那个是单次 RPC 的尺度（默认 30 秒），而这里是把一整份
// 资产在用户自己的上行带宽上传上去（视频上限 100 MiB），两者不是同一个时间尺度。
// 也不能不给上限——没有超时的上传会一直占着连接。
const directUploadTimeout = 30 * time.Minute

// directUpload 用一份直传凭证，把字节写到凭证指定的那个键上。
//
// 它是**命令行侧直传的唯一实现**，与浏览器的实现同源：两者读的是同一份凭证
// 定义（DirectUploadCredential），实现在各自的语言里，跨语言能共享的正是那份
// 形状（见 docs/design/objectstore/README.md）。
//
// 它刻意**不接收 context**：调用方手上那个是 RPC 的超时 context，用在这里会让
// 一份稍大的资产必然超时。超时由本函数自己给，好让"上传不跟着 RPC 的钟走"成为
// 结构上的事实，而不是一条要靠调用方记住的约定。
//
// 凭证字段（密钥与令牌）绝不进日志与错误信息：写进日志等于把一次写入的能力
// 留在了日志文件里。
func directUpload(credential *objectstorev1.DirectUploadCredential, data []byte, contentType string) error {
	if credential.GetKey() == "" || credential.GetSecretId() == "" {
		return fmt.Errorf("服务端没有下发可用的直传凭证")
	}
	bucketURL, err := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com",
		credential.GetBucket(), credential.GetRegion()))
	if err != nil {
		return fmt.Errorf("直传地址不合法: %w", err)
	}

	client := cos.NewClient(
		&cos.BaseURL{BucketURL: bucketURL},
		&http.Client{
			Timeout: directUploadTimeout,
			Transport: &cos.AuthorizationTransport{
				SecretID:     credential.GetSecretId(),
				SecretKey:    credential.GetSecretKey(),
				SessionToken: credential.GetSessionToken(),
			},
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), directUploadTimeout)
	defer cancel()

	if _, err := client.Object.Put(ctx, credential.GetKey(), bytes.NewReader(data), &cos.ObjectPutOptions{
		ACLHeaderOptions: &cos.ACLHeaderOptions{},
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
			ContentType:   contentType,
			ContentLength: int64(len(data)),
		},
	}); err != nil {
		return fmt.Errorf("直传失败：%s", describeUploadFailure(err))
	}
	return nil
}

// describeUploadFailure 把直传的失败压成一句可展示的文案。
//
// 只取错误码、消息与状态码：COS 的错误对象里还带着请求地址与请求头，把整个
// 错误打出来会把这次上传的细节（以及凭证可能经过的路径）留在终端与日志里。
func describeUploadFailure(err error) string {
	var cosErr *cos.ErrorResponse
	if !errors.As(err, &cosErr) {
		return "直传失败，请重试"
	}
	status := ""
	if cosErr.Response != nil {
		status = fmt.Sprintf("HTTP %d", cosErr.Response.StatusCode)
	}
	detail := ""
	for _, part := range []string{cosErr.Code, cosErr.Message, status} {
		if part == "" {
			continue
		}
		if detail != "" {
			detail += " / "
		}
		detail += part
	}
	if detail == "" {
		return "直传失败，请重试"
	}
	return detail
}
