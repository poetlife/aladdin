package cosupload

import (
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/objectstore"
)

const testBucketURL = "https://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com"

func mustParts(t *testing.T) bucketParts {
	t.Helper()
	parts, err := parseBucketURL(testBucketURL)
	if err != nil {
		t.Fatalf("解析桶地址失败: %v", err)
	}
	return parts
}

// 桶地址的形状决定了资源表达式里的地域、APPID 与桶名。形状对不上时必须拒绝
// 而不是退化成通配：一份把资源放宽到"这个账号下的一切"的策略，看起来与正常
// 策略一模一样。
func TestParseBucketURLRejectsWrongShapes(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"非 https", "http://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com"},
		{"没有主机名", "https://"},
		{"段数不对", "https://aladdin-1250000000.myqcloud.com"},
		{"不是 cos 域", "https://aladdin-1250000000.s3.ap-guangzhou.myqcloud.com"},
		{"缺少 APPID 后缀", "https://aladdin.cos.ap-guangzhou.myqcloud.com"},
		{"APPID 不是数字", "https://aladdin-abc.cos.ap-guangzhou.myqcloud.com"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseBucketURL(tt.url); err == nil {
				t.Errorf("桶地址 %q 被接受了，期望拒绝", tt.url)
			}
		})
	}
}

func TestParseBucketURLSplitsParts(t *testing.T) {
	parts := mustParts(t)
	if parts.Bucket != "aladdin-1250000000" {
		t.Errorf("桶名 = %q，期望含 APPID 的完整桶名", parts.Bucket)
	}
	if parts.Region != "ap-guangzhou" {
		t.Errorf("地域 = %q", parts.Region)
	}
	if parts.AppID != "1250000000" {
		t.Errorf("APPID = %q", parts.AppID)
	}
}

// 策略是这套机制的安全核心，因此它的每一项都要被断言，而不是"签发成功了就算
// 通过"。四条性质：只允许写、只有一个键、类型白名单、大小分档。
func TestBuildPolicyConstrainsActionResourceAndConditions(t *testing.T) {
	key := "galaxy/prj_abc/ast_def"
	rules := []objectstore.TypeRule{
		{ContentType: "image/png", MaxBytes: 10 << 20},
		{ContentType: "video/mp4", MaxBytes: 100 << 20},
	}
	policy, err := buildPolicy(mustParts(t), key, rules)
	if err != nil {
		t.Fatalf("构造策略失败: %v", err)
	}
	if policy.Version != "2.0" {
		t.Errorf("策略版本 = %q，期望 2.0", policy.Version)
	}
	if len(policy.Statement) != len(rules)+1 {
		t.Fatalf("策略项 %d 条，期望每条类型规则一条（%d）加一条显式拒绝（1）", len(policy.Statement), len(rules))
	}

	wantResource := "qcs::cos:ap-guangzhou:uid/1250000000:aladdin-1250000000/" + key
	allows := policy.Statement[:len(rules)]
	for i, statement := range allows {
		if statement.Effect != "allow" {
			t.Errorf("策略项 %d 的 effect = %q", i, statement.Effect)
		}
		if len(statement.Action) != 1 || statement.Action[0] != putObjectAction {
			t.Errorf("策略项 %d 的动作 = %v，期望只有 %s（只允许写）", i, statement.Action, putObjectAction)
		}
		if len(statement.Resource) != 1 || statement.Resource[0] != wantResource {
			t.Errorf("策略项 %d 的资源 = %v，期望恰好 %s", i, statement.Resource, wantResource)
		}
		gotType := statement.Condition[opStringLike][condContentType]
		if gotType != rules[i].ContentType {
			t.Errorf("策略项 %d 的类型条件 = %v，期望 %s", i, gotType, rules[i].ContentType)
		}
		if gotLen := statement.Condition[opNumericLessThanEqual][condContentLength]; gotLen != rules[i].MaxBytes {
			t.Errorf("策略项 %d 的长度条件 = %v，期望 %d", i, gotLen, rules[i].MaxBytes)
		}
	}

	// 最后一条是**显式拒绝**：临时凭证不得能声明权限（设 ACL、打标签），
	// 否则"公开权限只由上架路径设置"这句话不成立。
	deny := policy.Statement[len(policy.Statement)-1]
	if deny.Effect != "deny" {
		t.Errorf("最后一条策略项的 effect = %q，期望 deny", deny.Effect)
	}
	if len(deny.Action) != len(deniedActions) {
		t.Errorf("拒绝的动作 = %v，期望 %v", deny.Action, deniedActions)
	}
	if len(deny.Resource) != 1 || deny.Resource[0] != wantResource {
		t.Errorf("拒绝项的资源和允许项一样，只落在同一个键上：%v", deny.Resource)
	}

	// 资源里不得出现通配符：那会把"一个键"放宽成"一批键"。
	for _, statement := range policy.Statement {
		for _, resource := range statement.Resource {
			if strings.ContainsAny(resource, "*?") {
				t.Errorf("资源表达式含通配符: %s", resource)
			}
		}
	}
}

// 上限按类型分档：同一个键上，图片档与视频档各带自己的长度条件。
// "用视频的上限去卡图片"在策略里写不出来——这正是分档的实现方式。
func TestBuildPolicyKeepsLimitsPerType(t *testing.T) {
	policy, err := buildPolicy(mustParts(t), "avatars/usr_abc", []objectstore.TypeRule{
		{ContentType: "image/png", MaxBytes: 2 << 20},
		{ContentType: "image/gif", MaxBytes: 2 << 20},
	})
	if err != nil {
		t.Fatalf("构造策略失败: %v", err)
	}
	limits := map[string]interface{}{}
	for _, statement := range policy.Statement {
		contentType, _ := statement.Condition[opStringLike][condContentType].(string)
		limits[contentType] = statement.Condition[opNumericLessThanEqual][condContentLength]
	}
	for _, contentType := range []string{"image/png", "image/gif"} {
		if limits[contentType] != int64(2<<20) {
			t.Errorf("%s 的长度条件 = %v，期望 %d", contentType, limits[contentType], 2<<20)
		}
	}
}

func TestBuildPolicyRejectsUnusableInput(t *testing.T) {
	rules := []objectstore.TypeRule{{ContentType: "image/png", MaxBytes: 1 << 20}}
	cases := []struct {
		name  string
		key   string
		rules []objectstore.TypeRule
	}{
		{"空键", "", rules},
		{"键含通配符", "galaxy/prj_abc/*", rules},
		{"键含问号", "galaxy/prj_abc/ast?", rules},
		{"没有类型规则", "galaxy/prj_abc/ast_def", nil},
		{"类型为空", "galaxy/prj_abc/ast_def", []objectstore.TypeRule{{MaxBytes: 1 << 20}}},
		{"上限非正", "galaxy/prj_abc/ast_def", []objectstore.TypeRule{{ContentType: "image/png"}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildPolicy(mustParts(t), tt.key, tt.rules); err == nil {
				t.Error("期望拒绝，实际通过了")
			}
		})
	}
}
