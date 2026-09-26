package profile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
)

const subjectA = "usr_a"

// 几段真实到能被标准库嗅探出来的字节。
//
// 用真的签名而不是"随便几个字节"：这一层要挡的正是"看起来像图片、其实不是"，
// 而如果夹具本身过不了嗅探，测得的就是夹具而不是被测代码。
var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	jpegBytes = append([]byte("\xff\xd8\xff\xe0"), make([]byte, 64)...)
	gifBytes  = append([]byte("GIF89a"), make([]byte, 64)...)
	// 一段 HTML 与一段 SVG：都是"能顶着 image/png 存进去"的那类字节。
	htmlBytes = []byte("<html><body>这不是图片</body></html>")
	svgBytes  = []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)
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
	avatars    AvatarStore
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

// 未配置对象存储时，头像相关动作明确报"未启用"，而不是静默成功。
func TestAvatarUnavailableWithoutStore(t *testing.T) {
	profiles, _ := newTestProfiles(t, testOptions{register: true})

	if profiles.AvatarUploadEnabled() {
		t.Error("没给头像存储，却报告头像可用")
	}
	if _, err := profiles.SetAvatar(context.Background(), subjectA, pngBytes); !errors.Is(err, ErrAvatarUnavailable) {
		t.Errorf("err = %v，期望 ErrAvatarUnavailable", err)
	}

	// 昵称与简介不受影响：头像是可选能力，不是档案的前提。
	if _, err := profiles.Update(context.Background(), subjectA, "阿拉丁", ""); err != nil {
		t.Errorf("未启用头像不该影响昵称: %v", err)
	}
}

// 类型由字节本身判定，白名单之外一律拒绝——包括"能顶着 image/png 存进去"的那些。
func TestSniffAvatarType(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"PNG", pngBytes, "image/png"},
		{"JPEG", jpegBytes, "image/jpeg"},
		{"GIF", gifBytes, "image/gif"},
		{"HTML", htmlBytes, ""},
		// SVG 是 XML：即便放在 <img> 里不执行脚本，把它纳入白名单也等于给
		// 将来某次"改成直接打开"留下一颗雷。
		{"SVG", svgBytes, ""},
		{"空字节", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SniffAvatarType(tc.data)
			if tc.want == "" {
				if !errors.Is(err, ErrAvatarTypeNotAllowed) {
					t.Errorf("err = %v，期望 ErrAvatarTypeNotAllowed", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v，期望通过", err)
			}
			if got != tc.want {
				t.Errorf("类型 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 非图片字节被拒之后，**不留任何痕迹**：存储里没有对象，库里也没有键。
func TestSetAvatarRejectsAndLeavesNothing(t *testing.T) {
	avatars := NewMemoryAvatarStore()
	profiles, store := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	if _, err := profiles.SetAvatar(ctx, subjectA, htmlBytes); !errors.Is(err, ErrAvatarTypeNotAllowed) {
		t.Fatalf("err = %v，期望 ErrAvatarTypeNotAllowed", err)
	}
	if _, _, ok := avatars.Object(subjectA); ok {
		t.Error("被拒的字节仍然产生了对象")
	}

	view, err := profiles.Get(ctx, subjectA)
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if view.AvatarURL != "" {
		t.Errorf("AvatarURL = %q，期望为空", view.AvatarURL)
	}
	if stored, err := store.Get(ctx, subjectA); err == nil && stored.AvatarKey != "" {
		t.Errorf("AvatarKey = %q，期望为空", stored.AvatarKey)
	}
}

// 超过字节上限即拒绝，且**不产生对象**。
func TestSetAvatarRejectsTooLarge(t *testing.T) {
	avatars := NewMemoryAvatarStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})

	if _, err := profiles.SetAvatar(context.Background(), subjectA, make([]byte, AvatarMaxBytes+1)); !errors.Is(err, ErrAvatarTooLarge) {
		t.Errorf("err = %v，期望 ErrAvatarTooLarge", err)
	}
	if _, _, ok := avatars.Object(subjectA); ok {
		t.Error("超限的字节仍然产生了对象")
	}
}

// 一个主体一个对象键，替换即原地覆盖：不产生孤儿对象。
func TestSetAvatarReplacesInPlace(t *testing.T) {
	avatars := NewMemoryAvatarStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	if _, err := profiles.SetAvatar(ctx, subjectA, pngBytes); err != nil {
		t.Fatalf("首次上传失败: %v", err)
	}
	view, err := profiles.SetAvatar(ctx, subjectA, gifBytes)
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}

	contentType, data, ok := avatars.Object(subjectA)
	if !ok {
		t.Fatal("替换之后对象不见了")
	}
	if contentType != "image/gif" {
		t.Errorf("类型 = %q，期望被替换成 image/gif", contentType)
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
	avatars := NewMemoryAvatarStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	if _, err := profiles.ClearAvatar(ctx, subjectA); err != nil {
		t.Errorf("删除一个不存在的头像失败: %v", err)
	}

	if _, err := profiles.SetAvatar(ctx, subjectA, pngBytes); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	view, err := profiles.ClearAvatar(ctx, subjectA)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if view.AvatarURL != "" {
		t.Errorf("AvatarURL = %q，期望为空", view.AvatarURL)
	}
	if _, _, ok := avatars.Object(subjectA); ok {
		t.Error("对象没有被删掉")
	}
}

// 删头像**不动**昵称与简介：两件事互不相干。
func TestClearAvatarKeepsText(t *testing.T) {
	avatars := NewMemoryAvatarStore()
	profiles, _ := newTestProfiles(t, testOptions{register: true, avatars: avatars})
	ctx := context.Background()

	if _, err := profiles.Update(ctx, subjectA, "阿拉丁", "一盏灯"); err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	if _, err := profiles.SetAvatar(ctx, subjectA, pngBytes); err != nil {
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
	profiles, _ := newTestProfiles(t, testOptions{
		register: true,
		avatars:  failingAvatarStore{NewMemoryAvatarStore()},
	})
	ctx := context.Background()

	if _, err := profiles.Update(ctx, subjectA, "阿拉丁", ""); err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	if _, err := profiles.SetAvatar(ctx, subjectA, pngBytes); err != nil {
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

// failingAvatarStore 的写入成功、地址签发失败，用来验证降级路径。
//
// 它嵌入**指针**而不是值：内存实现里有一个 map，零值那个 map 是 nil，
// 值嵌入会让写入直接 panic——那样测的就不是降级，而是夹具自己的 bug。
type failingAvatarStore struct{ *MemoryAvatarStore }

func (failingAvatarStore) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("签发失败")
}
