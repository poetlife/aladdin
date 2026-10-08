// Command relay 把本机构建出来的发布产物放到一个**服务器够得着的地址**下，
// 供 deploy.sh 的 --from 去取。
//
// 它只解决"传不动"这一件事，不解决"部署"：校验和、备份、原子替换、健康检查、
// 失败回滚仍然只在 deploy/deploy.sh 那一处（见 docs/ssot-registry.md 的
// 「副作用类」）。产物到了中转地址之后，服务器上跑的仍是那一个脚本。
//
// 什么时候用它：本机到服务器的上行很差（一份服务端产物约 10MB，传不过去），
// 或者服务器取不到 GitHub。中转地址在哪无所谓——对象存储、内网 HTTP 目录都行，
// 只要三份产物在**同一个地址**下、命名与 make release-build 出来的一致。
//
// **为什么中转对象置成公开读**：deploy.sh 取的是一个**基地址**，三个文件名由
// 它自己拼出来；而预签名地址是按对象签的，签不出"一个基地址"。代价是这几个
// 对象谁拿到地址都能下，因此在**用完就要删掉**（第二条命令）。
//
// 用法（在仓库根目录执行；凭据走环境变量，与本地开发用的是同一对，
// 加载方式见 Makefile 的 DEV_ENV_LOAD）：
//
//	go run ./deploy/relay push <桶地址> <远端前缀> <本地文件>…
//	go run ./deploy/relay rm   <桶地址> <远端前缀> <对象名>…
//
// rm 只用到参数的文件名部分，因此把 push 那三个路径原样再贴一次即可。
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tencentyun/cos-go-sdk-v5"
)

const usage = `用法：
  relay push <桶地址> <远端前缀> <本地文件>…
  relay rm   <桶地址> <远端前缀> <对象名>…
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 4 {
		return fmt.Errorf("参数不足\n%s", usage)
	}
	mode, bucketURL, prefix := args[0], args[1], strings.Trim(args[2], "/")
	if prefix == "" {
		return fmt.Errorf("远端前缀不能为空：中转地址是一个目录，得能一次删干净\n%s", usage)
	}
	if mode != "push" && mode != "rm" {
		return fmt.Errorf("未知的子命令 %q\n%s", mode, usage)
	}
	names := args[3:]

	secretID, secretKey := os.Getenv("ALADDIN_COS_SECRET_ID"), os.Getenv("ALADDIN_COS_SECRET_KEY")
	if secretID == "" || secretKey == "" {
		return fmt.Errorf("缺少 ALADDIN_COS_SECRET_ID / ALADDIN_COS_SECRET_KEY，" +
			"本机放在 .env.local（加载方式见 Makefile 的 DEV_ENV_LOAD）")
	}
	bucket, err := url.Parse(bucketURL)
	if err != nil {
		return fmt.Errorf("桶地址无法解析：%w", err)
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: bucket}, &http.Client{
		// 上限给得远大于预计：这不是"预计要多久"的估算，而是"别在网络慢的
		// 时候先于网络断掉"的保险丝——用这个工具的前提本来就是网络不好。
		Timeout: 30 * time.Minute,
		Transport: &cos.AuthorizationTransport{
			SecretID:  secretID,
			SecretKey: secretKey,
		},
	})
	ctx := context.Background()

	if mode == "rm" {
		return remove(ctx, client, prefix, names)
	}
	return push(ctx, client, bucketURL, prefix, names)
}

// push 逐个上传并按对象置公开读。
//
// 走 SDK 的 PutFromFile 而不是自己开文件：它自带重试，而这个工具的前提本来就是
// 网络不好——重试在这里不是优化，是"传输失败不用从头再来一遍"。
//
// 全部传完才打印中转地址：半路失败时给出一个"看起来可用"的地址是有害的，
// 因为 deploy.sh 拿到它只会以校验和不匹配收场，而那时人已经在另一个终端上。
func push(ctx context.Context, client *cos.Client, bucketURL, prefix string, files []string) error {
	names := make([]string, 0, len(files))
	for _, path := range files {
		key := prefix + "/" + filepath.Base(path)
		_, err := client.Object.PutFromFile(ctx, key, path, &cos.ObjectPutOptions{
			ACLHeaderOptions: &cos.ACLHeaderOptions{XCosACL: cos.ACL.PublicRead},
		})
		if err != nil {
			return fmt.Errorf("上传 %s 失败：%w", key, err)
		}
		names = append(names, filepath.Base(path))
		fmt.Printf("已上传（公开读）%s\n", key)
	}

	base := strings.TrimSuffix(bucketURL, "/") + "/" + prefix
	fmt.Printf("\n中转地址（填给 deploy.sh 的 --from）：\n  %s\n", base)
	fmt.Printf("\n验证完删掉（公开读的对象不该留在桶里）：\n  go run ./deploy/relay rm %s %s %s\n",
		bucketURL, prefix, strings.Join(names, " "))
	return nil
}

// remove 按对象名删除。
//
// 它只用到参数的文件名部分：中转是一组临时对象，调用方手上未必还留着刚上传的
// 那几个文件，但文件名（也就是键）是同一套，因此 push 的参数可以原样再贴一次。
func remove(ctx context.Context, client *cos.Client, prefix string, names []string) error {
	for _, name := range names {
		key := prefix + "/" + filepath.Base(name)
		if _, err := client.Object.Delete(ctx, key); err != nil {
			return fmt.Errorf("删除 %s 失败：%w", key, err)
		}
		fmt.Printf("已删除 %s\n", key)
	}
	return nil
}
