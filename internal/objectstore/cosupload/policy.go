package cosupload

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/tencentyun/cos-go-sdk-v5"

	"github.com/poetlife/aladdin/internal/objectstore"
)

const (
	// putObjectAction 是策略里唯一允许的动作。
	//
	// **只有写。** 读由另一条路径承担（签发的读取地址），而能取字节的凭证在
	// 有效期内是一份谁拿到都能用的东西——上传路径不需要它。
	putObjectAction = "name/cos:PutObject"

	// 条件运算符。取值来自对象存储的策略语法：
	//
	//   - numeric_less_than_equal：数值上界，用来卡请求体长度；
	//   - string_like：字符串模式，用来卡声明的内容类型。
	//
	// 两者一起构成"类型与它对应的大小上限"这条约束——**它们在同一条策略项
	// 里**，因此"用视频的上限去卡图片"在策略层面就写不出来。
	opNumericLessThanEqual = "numeric_less_than_equal"
	opStringLike           = "string_like"

	// condContentLength 是请求体长度这个条件键。
	condContentLength = "cos:content-length"
	// condContentType 是声明的内容类型这个条件键。
	//
	// **它校验的是请求里声明的类型，不是字节本身。** 对象存储不做内容嗅探，
	// 因此一份字节可以顶着 image/png 的名字存进去；这条条件的作用是让**存下来
	// 的类型必属白名单**，于是公开区上的对象永远以一个无害类型下发。
	condContentType = "cos:content-type"
)

// deniedActions 是策略里**显式拒绝**的动作，落在同一个资源上。
//
// 它是 spec 那条硬性约束的落点：**签发出去的临时凭证不得能声明权限**（不能设
// ACL、不能让渡所有权）。否则"公开权限只由上架路径设置"这句话不成立，而它正是
// 逐对象公开读赖以成立的前提——同时它也是绕过发布校验的一条口子：一个能自己把
// 对象设成公开读的上传方，完全可以不走引用完整性这道关。
//
// **它是一条尽力而为的兜底，不是完整的证明。** 对象存储的策略条件能约束的东西
// 有限（比如"禁止覆盖"没有对应的条件键，只能由客户端在请求上带条件、由服务端
// 在提交时读回核对）。这条边界与"桶真的照做了策略只能在部署后冒烟里验"是同一处。
var deniedActions = []string{
	"name/cos:PutObjectACL",
	"name/cos:PutObjectTagging",
}

// bucketParts 是一个桶地址解析出来的三样东西。
//
// 资源表达式需要 region、APPID 与桶名三者，而配置里给的是一个完整主机名，
// 因此解析只在这一处发生。
type bucketParts struct {
	// Bucket 是桶名（含 APPID），客户端 SDK 要的就是它。
	Bucket string
	Region string
	AppID  string
}

// parseBucketURL 从桶地址里解析出桶名、地域与 APPID（唯一入口）。
//
// 只接受形如 `https://<桶名>-<APPID>.cos.<地域>.myqcloud.com` 的取值——这正是
// 配置文档里给的形状。形状对不上时**拒绝**而不是退化成 `*` 通配：一份把资源
// 放宽到"这个账号下的一切"的策略，看起来与正常策略一模一样。
func parseBucketURL(raw string) (bucketParts, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return bucketParts{}, fmt.Errorf("解析桶地址失败: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return bucketParts{}, fmt.Errorf("桶地址必须是带主机名的 https 地址，当前是 %q", raw)
	}
	labels := strings.Split(parsed.Host, ".")
	if len(labels) != 5 || labels[1] != "cos" || labels[3] != "myqcloud" || labels[4] != "com" {
		return bucketParts{}, fmt.Errorf("桶地址的形状不是 <桶名>-<APPID>.cos.<地域>.myqcloud.com，当前是 %q", raw)
	}
	bucket := labels[0]
	region := labels[2]
	if bucket == "" || region == "" {
		return bucketParts{}, fmt.Errorf("桶地址缺少桶名或地域，当前是 %q", raw)
	}
	// APPID 是桶名最后一段数字（`mybucket-1250000000`）。
	dash := strings.LastIndex(bucket, "-")
	if dash < 0 || dash == len(bucket)-1 {
		return bucketParts{}, fmt.Errorf("桶名缺少 -<APPID> 后缀，当前是 %q", bucket)
	}
	appID := bucket[dash+1:]
	for i := 0; i < len(appID); i++ {
		if appID[i] < '0' || appID[i] > '9' {
			return bucketParts{}, fmt.Errorf("桶名的 APPID 不是数字，当前是 %q", bucket)
		}
	}
	return bucketParts{Bucket: bucket, Region: region, AppID: appID}, nil
}

// putObjectResource 返回"恰好这一个对象"的资源表达式。
//
// 它是**约束的核心**：临时凭证能写什么完全由它决定，而它只含一个键。
func putObjectResource(parts bucketParts, key string) string {
	return fmt.Sprintf("qcs::cos:%s:uid/%s:%s/%s", parts.Region, parts.AppID, parts.Bucket, key)
}

// buildPolicy 构造限定"只写一个键、且类型与大小受条件约束"的策略（唯一入口）。
//
// 每条规则对应一条允许项：动作只有写入，资源只有那一个键，条件带上该类型的
// 长度上界与该类型本身。多条规则之间是**或**的关系（任一条满足即放行），因此
// "图片档 10 MiB、视频档 100 MiB"这类分档可以并列表达。
func buildPolicy(parts bucketParts, key string, rules []objectstore.TypeRule) (*cos.CredentialPolicy, error) {
	if key == "" {
		return nil, fmt.Errorf("对象键不能为空")
	}
	// 键里的通配符会把资源从"一个键"放宽成"一批键"。对象的键由服务端的键
	// 规则生成，出现通配符只可能是调用方拼错了——拒绝，不猜。
	if strings.ContainsAny(key, "*?") {
		return nil, fmt.Errorf("对象键不得含通配符，当前是 %q", key)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("至少需要一条类型规则：没有它，"+
			"签发出去的凭证不限制上传的类型与大小，%s 也就不成立了", condContentType)
	}

	resource := putObjectResource(parts, key)
	statements := make([]cos.CredentialPolicyStatement, 0, len(rules)+1)
	for _, rule := range rules {
		if rule.ContentType == "" {
			return nil, fmt.Errorf("类型规则的内容类型不能为空")
		}
		if rule.MaxBytes <= 0 {
			return nil, fmt.Errorf("类型规则 %s 的大小上限必须为正", rule.ContentType)
		}
		statements = append(statements, cos.CredentialPolicyStatement{
			Action:   []string{putObjectAction},
			Effect:   "allow",
			Resource: []string{resource},
			Condition: map[string]map[string]interface{}{
				opStringLike:           {condContentType: rule.ContentType},
				opNumericLessThanEqual: {condContentLength: rule.MaxBytes},
			},
		})
	}
	// 显式拒绝"能声明权限"的动作，落在同一个资源上（见 deniedActions）。
	statements = append(statements, cos.CredentialPolicyStatement{
		Action:   append([]string(nil), deniedActions...),
		Effect:   "deny",
		Resource: []string{resource},
	})
	return &cos.CredentialPolicy{Version: "2.0", Statement: statements}, nil
}
