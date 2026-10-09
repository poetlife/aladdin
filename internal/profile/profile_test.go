package profile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/imagetype"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/rbac"
)

const subjectA = "usr_a"

// 几段假的图片字节。
//
// **类型不再由字节决定**：直传之后服务端看不到字节，媒体类型是上传方声明的
// （见 docs/design/objectstore/README.md）。这些字节因此在测试里只充当"客户端
// 传上去的那份内容"，形状不再有任何含义。
var (
	pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	gifBytes = append([]byte("GIF89a"), make([]byte, 64)...)
)

// fakeIdentities 是 IdentityLister 的假实现，顺序完全由用例给定。
//
// 真实现会排序（那是身份模块的职责），这里刻意不排：本模块的规则是
// "取列表里第一条非空的"，而顺序从哪来不该由它决定。
type fakeIdentities struct {
	list []identity.Identity
	err  error
}

func (f fakeIdentities) List(context.Context, string) ([]identity.Identity, error) {
	return f.list, f.err
}

// display 造一个只带展示值的渠道身份。
func display(value string) identity.Identity {
	return identity.Identity{Source: identity.SourceGoogle, ExternalID: value, Display: value}
}

type testOptions struct {
	identities IdentityLister
	avatars    objectstore.Store
	register   bool
}

// newTestProfiles 构造一个接在内存存储上的档案入口。
func newTestProfiles(t *testing.T, opts testOptions) (*Profiles, *MemoryStore) {
	t.Helper()

	subjects := rbac.NewMemoryStore()
	if opts.register {
		if err := subjects.PutSubject(context.Background(), rbac.Subject{
			ID: subjectA, Type: rbac.SubjectTypeUser,
		}); err != nil {
			t.Fatalf("登记测试主体失败: %v", err)
		}
	}

	store := NewMemoryStore()
	profiles := NewProfiles(ProfilesDeps{
		Store:      store,
		Subjects:   subjects,
		Identities: opts.identities,
		Avatars:    opts.avatars,
		Logger:     zap.NewNop(),
	})
	return profiles, store
}

// 没设昵称时回退到渠道可读标识。这是"界面上不会显示内部标识"的落点。
func TestDisplayNameFallsBackToChannel(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{
		register:   true,
		identities: fakeIdentities{list: []identity.Identity{display("a@example.com")}},
	})

	view, err := profiles.Get(context.Background(), subjectA)
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if view.DisplayName != "a@example.com" {
		t.Errorf("DisplayName = %q，期望回退到渠道标识", view.DisplayName)
	}
}

// 昵称优先于渠道标识。
func TestDisplayNamePrefersNickname(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{
		register:   true,
		identities: fakeIdentities{list: []identity.Identity{display("a@example.com")}},
	})

	view, err := profiles.Update(context.Background(), subjectA, "阿拉丁", "一盏灯")
	if err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	if view.DisplayName != "阿拉丁" {
		t.Errorf("DisplayName = %q，期望昵称", view.DisplayName)
	}
	if view.Nickname != "阿拉丁" || view.Bio != "一盏灯" {
		t.Errorf("读回 = %+v，与写入的不一致", view.Profile)
	}
}

// 连渠道标识都没有（未登记主体、或渠道没给可读标识）时回退到主体标识。
//
// 显示内部标识不理想，但比显示空字符串好——后者在界面上像是渲染坏了。
func TestDisplayNameFallsBackToSubjectID(t *testing.T) {
	t.Run("没有渠道", func(t *testing.T) {
		profiles, _ := newTestProfiles(t, testOptions{register: true})
		view, err := profiles.Get(context.Background(), subjectA)
		if err != nil {
			t.Fatalf("读取档案失败: %v", err)
		}
		if view.DisplayName != subjectA {
			t.Errorf("DisplayName = %q，期望 %q", view.DisplayName, subjectA)
		}
	})

	t.Run("渠道没有可读标识", func(t *testing.T) {
		profiles, _ := newTestProfiles(t, testOptions{
			register:   true,
			identities: fakeIdentities{list: []identity.Identity{display("")}},
		})
		view, err := profiles.Get(context.Background(), subjectA)
		if err != nil {
			t.Fatalf("读取档案失败: %v", err)
		}
		if view.DisplayName != subjectA {
			t.Errorf("DisplayName = %q，期望 %q", view.DisplayName, subjectA)
		}
	})

	t.Run("身份模块缺席", func(t *testing.T) {
		profiles, _ := newTestProfiles(t, testOptions{register: true})
		view, err := profiles.Get(context.Background(), subjectA)
		if err != nil {
			t.Fatalf("读取档案失败: %v", err)
		}
		if view.DisplayName != subjectA {
			t.Errorf("DisplayName = %q，期望 %q", view.DisplayName, subjectA)
		}
	})
}

// 回退的第二步取的是**列表里第一条非空的**渠道可读标识，顺序由身份模块定。
func TestDisplayNameSkipsEmptyChannelIdentifiers(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{
		register: true,
		identities: fakeIdentities{list: []identity.Identity{
			display(""),
			display("second@example.com"),
			display("third@example.com"),
		}},
	})

	view, err := profiles.Get(context.Background(), subjectA)
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if view.DisplayName != "second@example.com" {
		t.Errorf("DisplayName = %q，期望第一条非空的那个", view.DisplayName)
	}
}

// 批量读取与逐条读取给出**同一份结果**。
//
// 这条用例钉的是"回退规则只有一处实现"：批量版若另写一份拼接（先批量取昵称、
// 再各自补渠道标识），它迟早会与逐条那份不一致，而漂移的表现是"同一个人在两个
// 页面上叫两个名字"。
func TestGetManyMatchesGet(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{
		register:   true,
		identities: fakeIdentities{list: []identity.Identity{display("a@example.com")}},
	})
	ctx := context.Background()
	if _, err := profiles.Update(ctx, subjectA, "阿拉丁", ""); err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	// 第二个主体没有档案行：**它不是错误**，展示名按回退规则落到渠道标识。
	const subjectB = "usr_b"

	views, err := profiles.GetMany(ctx, []string{subjectA, subjectB, subjectA, ""})
	if err != nil {
		t.Fatalf("批量读取失败: %v", err)
	}
	// 去重且丢空串：两个主体各一项，空串不占键。
	if len(views) != 2 {
		t.Fatalf("项数 = %d，期望 2（去重、丢空串）", len(views))
	}
	for _, subjectID := range []string{subjectA, subjectB} {
		single, err := profiles.Get(ctx, subjectID)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", subjectID, err)
		}
		if views[subjectID] != single {
			t.Errorf("%s 的批量结果 = %+v，与逐条读取的 %+v 不一致", subjectID, views[subjectID], single)
		}
	}
	if views[subjectA].DisplayName != "阿拉丁" {
		t.Errorf("有昵称的那一个 DisplayName = %q，期望昵称", views[subjectA].DisplayName)
	}
	if views[subjectB].DisplayName != "a@example.com" {
		t.Errorf("没档案的那一个 DisplayName = %q，期望回退到渠道标识", views[subjectB].DisplayName)
	}
}

// 空输入不报错，也不去查存储：调用方不必为"这一页一条带主体的都没有"单独分支。
func TestGetManyWithNothingToResolve(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{})
	views, err := profiles.GetMany(context.Background(), nil)
	if err != nil {
		t.Fatalf("空输入不应失败: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("项数 = %d，期望 0", len(views))
	}
}

// 存储用不了时**整体失败、不返回部分结果**。
//
// 只回一半会让调用方把"这次解析没做成"读成"另外那些人没有名字"——降级必须是一个
// 要么全有要么全无的结论，否则界面上会出现一半有名字、一半是标识的假象。
func TestGetManyFailsWholeWhenStoreUnavailable(t *testing.T) {
	profiles := NewProfiles(ProfilesDeps{
		Store:  unavailableStore{},
		Logger: zap.NewNop(),
	})
	views, err := profiles.GetMany(context.Background(), []string{subjectA, "usr_b"})
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("err = %v，期望包着 ErrStoreUnavailable", err)
	}
	if views != nil {
		t.Errorf("返回了部分结果 %+v，期望 nil", views)
	}
}

// unavailableStore 让读取一律失败。
type unavailableStore struct{ Store }

func (unavailableStore) Get(context.Context, string) (Profile, error) {
	return Profile{}, ErrStoreUnavailable
}

// 空值就是"未设置"：清空昵称之后展示名回到回退序列的下一条。
func TestUpdateClearsAndTrims(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{
		register:   true,
		identities: fakeIdentities{list: []identity.Identity{display("a@example.com")}},
	})
	ctx := context.Background()

	// 首尾空白被去掉：一个只由空格组成的昵称应当被判成"清空"，
	// 而不是"设了一个看不见的名字"。
	view, err := profiles.Update(ctx, subjectA, "  阿拉丁  ", "  一盏灯  ")
	if err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	if view.Nickname != "阿拉丁" || view.Bio != "一盏灯" {
		t.Errorf("读回 = %+v，期望去掉首尾空白", view.Profile)
	}

	view, err = profiles.Update(ctx, subjectA, "   ", "")
	if err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if view.Nickname != "" || view.Bio != "" {
		t.Errorf("清空后 = %+v，期望两个字段都为空", view.Profile)
	}
	if view.DisplayName != "a@example.com" {
		t.Errorf("DisplayName = %q，清空昵称后应当回退", view.DisplayName)
	}
}

// 长度上限**按字符数**算，不是字节数。
//
// 按字节算会让"一个中文昵称写到十几个字就被拒"成为一条需要向用户解释的规则。
func TestUpdateEnforcesRuneLimits(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{register: true})
	ctx := context.Background()

	atLimit := strings.Repeat("字", NicknameMaxRunes)
	if _, err := profiles.Update(ctx, subjectA, atLimit, ""); err != nil {
		t.Errorf("%d 字的昵称被拒: %v", NicknameMaxRunes, err)
	}

	overLimit := strings.Repeat("字", NicknameMaxRunes+1)
	if _, err := profiles.Update(ctx, subjectA, overLimit, ""); !errors.Is(err, ErrNicknameTooLong) {
		t.Errorf("err = %v，期望 ErrNicknameTooLong", err)
	}

	if _, err := profiles.Update(ctx, subjectA, "", strings.Repeat("字", BioMaxRunes+1)); !errors.Is(err, ErrBioTooLong) {
		t.Errorf("err = %v，期望 ErrBioTooLong", err)
	}
}

// 未登记的主体**不产生档案行**。
//
// 少了这一步，一份过期的会话就能在库里留下一条永远无人认领的档案。
func TestUpdateRequiresRegisteredSubject(t *testing.T) {
	profiles, store := newTestProfiles(t, testOptions{register: false})

	_, err := profiles.Update(context.Background(), "usr_从未登记", "阿拉丁", "")
	if !errors.Is(err, rbac.ErrSubjectNotFound) {
		t.Errorf("err = %v，期望 ErrSubjectNotFound", err)
	}
	if _, err := store.Get(context.Background(), "usr_从未登记"); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("未登记主体的档案行被创建了: %v", err)
	}
}

// uploadAvatar 走一遍直传：签发、把字节写进假存储（模拟客户端直传）、提交。
//
// 服务端在整条路径上**不接触字节**，因此夹具里那一步 Put 扮演的是浏览器的角色
// （见 docs/design/objectstore/README.md）。
func uploadAvatar(t *testing.T, profiles *Profiles, objects *objectstore.MemoryStore, subjectID, contentType string, data []byte) (View, error) {
	t.Helper()
	ctx := context.Background()
	if _, err := profiles.BeginAvatarUpload(ctx, subjectID, contentType, int64(len(data))); err != nil {
		return View{}, err
	}
	objects.SimulateUpload(AvatarKey(subjectID), data)
	return profiles.CommitAvatarUpload(ctx, subjectID)
}

// 未配置对象存储时，头像相关动作明确报"未启用"，而不是静默成功。
func TestAvatarUnavailableWithoutStore(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{register: true})
	ctx := context.Background()

	if profiles.AvatarUploadEnabled() {
		t.Error("没给头像存储，却报告头像可用")
	}
	if _, err := profiles.BeginAvatarUpload(ctx, subjectA, "image/png", int64(len(pngBytes))); !errors.Is(err, ErrAvatarUnavailable) {
		t.Errorf("签发 err = %v，期望 ErrAvatarUnavailable", err)
	}
	if _, err := profiles.CommitAvatarUpload(ctx, subjectA); !errors.Is(err, ErrAvatarUnavailable) {
		t.Errorf("提交 err = %v，期望 ErrAvatarUnavailable", err)
	}

	// 昵称与简介不受影响：头像是可选能力，不是档案的前提。
	if _, err := profiles.Update(ctx, subjectA, "阿拉丁", ""); err != nil {
		t.Errorf("未启用头像不该影响昵称: %v", err)
	}
}

// 声明的类型必须在白名单内，否则**拒于签发这一步**。
//
// 直传之后服务端看不到字节，因此白名单从"字节确实是图片"降级成了"下发时的类型
// 必属一个无害集合"（见 docs/design/objectstore/README.md）。白名单里刻意没有
// 任何可执行类型，测试也把这三种钉住。
func TestBeginAvatarUploadChecksDeclaredType(t *testing.T) {
	allowed := []struct {
		name     string
		declared string
		want     string
	}{
		{"PNG", "image/png", "image/png"},
		{"JPEG", "image/jpeg", "image/jpeg"},
		{"GIF", "image/gif", "image/gif"},
		// 归一化：大小写与参数都算同一个类型。
		{"大写", "IMAGE/PNG", "image/png"},
		{"带参数", "image/jpeg; charset=binary", "image/jpeg"},
	}
	rejected := []struct {
		name     string
		declared string
	}{
		{"HTML", "text/html"},
		// SVG 是 XML，可以内嵌脚本：它在下发路径上的隔离需要单独论证，首版不收。
		{"SVG", "image/svg+xml"},
		{"不在白名单的图片", "image/webp"},
		{"空", ""},
	}

	for _, tc := range allowed {
		t.Run("接受/"+tc.name, func(t *testing.T) {
			avatars := objectstore.NewMemoryStore()
			profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})

			if _, err := profiles.BeginAvatarUpload(context.Background(), subjectA, tc.declared, 8); err != nil {
				t.Fatalf("err = %v，期望通过", err)
			}
			// 签发出去的类型必须是**归一化之后**的那一个：库里、存储上、下发时
			// 都该是同一个字符串。
			issued := avatars.Issued()
			if len(issued) != 1 {
				t.Fatalf("签发 %d 次，期望 1 次", len(issued))
			}
			if issued[0].Key != AvatarKey(subjectA) {
				t.Errorf("键 = %q，期望 %q（一个主体一个键）", issued[0].Key, AvatarKey(subjectA))
			}
			assertAvatarRules(t, issued[0].Rules)
		})
	}

	for _, tc := range rejected {
		t.Run("拒绝/"+tc.name, func(t *testing.T) {
			avatars := objectstore.NewMemoryStore()
			profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})

			if _, err := profiles.BeginAvatarUpload(context.Background(), subjectA, tc.declared, 8); !errors.Is(err, ErrAvatarTypeNotAllowed) {
				t.Errorf("err = %v，期望 ErrAvatarTypeNotAllowed", err)
			}
			if len(avatars.Issued()) != 0 {
				t.Error("被拒的类型仍然签发了凭证")
			}
		})
	}
}

// assertAvatarRules 断言签发给存储的类型规则就是白名单本身。
//
// 规则**由白名单派生**：两处各写一份的表现是"服务端接受了、存储侧拒绝"
// （或反过来），而用户看到的是一句无法归因的失败。
func assertAvatarRules(t *testing.T, rules []objectstore.TypeRule) {
	t.Helper()
	if len(rules) != len(imagetype.Allowed()) {
		t.Fatalf("规则 %d 条，期望与白名单一样多（%d）", len(rules), len(imagetype.Allowed()))
	}
	got := map[string]int64{}
	for _, rule := range rules {
		got[rule.ContentType] = rule.MaxBytes
	}
	for _, contentType := range imagetype.Allowed() {
		if got[contentType] != AvatarMaxBytes {
			t.Errorf("%s 的上限 = %d，期望 %d", contentType, got[contentType], AvatarMaxBytes)
		}
	}
}

// 声明超过上限即拒绝签发：这是**给用户的省事**（别把一个几十 MB 的文件整个传
// 上来只为了被拒），不是安全边界——真正的上限由存储侧执行。
func TestBeginAvatarUploadRejectsTooLargeDeclaration(t *testing.T) {
	avatars := objectstore.NewMemoryStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})

	_, err := profiles.BeginAvatarUpload(context.Background(), subjectA, "image/png", AvatarMaxBytes+1)
	if !errors.Is(err, ErrAvatarTooLarge) {
		t.Errorf("err = %v，期望 ErrAvatarTooLarge", err)
	}
	if len(avatars.Issued()) != 0 {
		t.Error("超限的声明仍然签发了凭证")
	}
}

// 提交核对**真实**字节数：存储侧已经按声明卡过一次，这里核对的是服务端自己的
// 结论，而核对不过的对象会被删掉。
func TestCommitAvatarUploadRejectsOversizedObject(t *testing.T) {
	avatars := objectstore.NewMemoryStore()
	profiles, store := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	// 声明一个合法大小，实际却传了超过上限的字节。
	if _, err := profiles.BeginAvatarUpload(ctx, subjectA, "image/png", 1024); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	avatars.SimulateUpload(AvatarKey(subjectA), make([]byte, AvatarMaxBytes+1))

	if _, err := profiles.CommitAvatarUpload(ctx, subjectA); !errors.Is(err, ErrAvatarTooLarge) {
		t.Fatalf("err = %v，期望 ErrAvatarTooLarge", err)
	}
	if _, err := avatars.Head(ctx, AvatarKey(subjectA)); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("超限的对象没有被删掉")
	}
	// 库里也不能留下键：库内是"这个主体有没有头像"的权威。
	if stored, err := store.Get(ctx, subjectA); err == nil && stored.AvatarKey != "" {
		t.Errorf("AvatarKey = %q，期望为空", stored.AvatarKey)
	}
}

// 提交核对存在性：对象不在就报"上传可能没有完成"，而不是把它当成一次成功。
func TestCommitAvatarUploadRequiresObject(t *testing.T) {
	avatars := objectstore.NewMemoryStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	if _, err := profiles.BeginAvatarUpload(ctx, subjectA, "image/png", 8); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	// 刻意不 Put：模拟上传中断。
	if _, err := profiles.CommitAvatarUpload(ctx, subjectA); !errors.Is(err, ErrAvatarObjectMissing) {
		t.Errorf("err = %v，期望 ErrAvatarObjectMissing", err)
	}
}

// 一个主体一个键，替换即原地覆盖：不产生孤儿对象。
func TestAvatarUploadReplacesInPlace(t *testing.T) {
	avatars := objectstore.NewMemoryStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	if _, err := uploadAvatar(t, profiles, avatars, subjectA, "image/png", pngBytes); err != nil {
		t.Fatalf("首次上传失败: %v", err)
	}
	view, err := uploadAvatar(t, profiles, avatars, subjectA, "image/gif", gifBytes)
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}

	// 两次签发落在**同一个键**上，因此桶上只有一个对象（对象存储的覆盖写）。
	issued := avatars.Issued()
	if len(issued) != 2 {
		t.Fatalf("签发 %d 次，期望 2 次", len(issued))
	}
	if issued[0].Key != issued[1].Key {
		t.Errorf("两次上传的键不同（%q / %q），替换会产生孤儿对象", issued[0].Key, issued[1].Key)
	}
	data, err := avatars.Read(ctx, AvatarKey(subjectA))
	if err != nil {
		t.Fatalf("读取对象失败: %v", err)
	}
	if len(data) != len(gifBytes) {
		t.Errorf("字节数 = %d，期望 %d", len(data), len(gifBytes))
	}
	// 地址的具体形状由存储实现定，这里只要求它不是空的。
	if view.AvatarURL == "" {
		t.Error("AvatarURL 为空，期望有地址")
	}
}

// 删头像幂等：本来就没有头像时也成功。
func TestClearAvatarIsIdempotent(t *testing.T) {
	avatars := objectstore.NewMemoryStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	if _, err := profiles.ClearAvatar(ctx, subjectA); err != nil {
		t.Errorf("删除一个不存在的头像失败: %v", err)
	}

	if _, err := uploadAvatar(t, profiles, avatars, subjectA, "image/png", pngBytes); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	view, err := profiles.ClearAvatar(ctx, subjectA)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if view.AvatarURL != "" {
		t.Errorf("AvatarURL = %q，期望为空", view.AvatarURL)
	}
	if _, err := avatars.Head(ctx, AvatarKey(subjectA)); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("对象没有被删掉")
	}
}

// 删头像**不动**昵称与简介：两件事互不相干。
func TestClearAvatarKeepsText(t *testing.T) {
	avatars := objectstore.NewMemoryStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	if _, err := profiles.Update(ctx, subjectA, "阿拉丁", "一盏灯"); err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	if _, err := uploadAvatar(t, profiles, avatars, subjectA, "image/png", pngBytes); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	view, err := profiles.ClearAvatar(ctx, subjectA)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if view.Nickname != "阿拉丁" || view.Bio != "一盏灯" {
		t.Errorf("删头像后 = %+v，昵称与简介不该变", view.Profile)
	}
}

// 地址签发失败**不让整个档案读取失败**：昵称与简介与头像无关，为了一张图
// 让页头连名字都显示不出来，是拿次要功能去挡主要功能。
func TestGetDegradesWhenAvatarURLFails(t *testing.T) {
	avatars := objectstore.NewMemoryStore()
	profiles, _ := newTestProfiles(t, testOptions{
		register: true,
		avatars:  failingPresignStore{avatars},
	})
	ctx := context.Background()

	if _, err := profiles.Update(ctx, subjectA, "阿拉丁", ""); err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	if _, err := uploadAvatar(t, profiles, avatars, subjectA, "image/png", pngBytes); err != nil {
		t.Fatalf("上传失败: %v", err)
	}

	view, err := profiles.Get(ctx, subjectA)
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if view.AvatarURL != "" {
		t.Errorf("AvatarURL = %q，期望降级为空", view.AvatarURL)
	}
	if view.DisplayName != "阿拉丁" {
		t.Errorf("DisplayName = %q，期望不受头像影响", view.DisplayName)
	}
}

// failingPresignStore 的直传一切正常、只有地址签发失败，用来验证降级路径。
//
// 它嵌入**指针**而不是值：内存实现里有一个 map，值嵌入会让写入直接 panic——
// 那样测的就不是降级，而是夹具自己的 bug。
type failingPresignStore struct{ *objectstore.MemoryStore }

func (failingPresignStore) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("签发失败")
}
