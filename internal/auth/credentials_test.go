package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateHome 把凭证目录指到临时目录，避免测试写到真实的家目录里。
//
// 同时设 HOME 与 XDG_CONFIG_HOME：os.UserConfigDir 在 macOS 上用前者、
// 在 Linux 上用后者，只设一个会让另一个平台上的测试落到真实目录。
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
}

func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvToken, "")
	t.Setenv(EnvScope, "")
}

func credentialPath(t *testing.T) string {
	t.Helper()
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("取凭证路径失败: %v", err)
	}
	return path
}

func TestSaveAndResolveRoundTrip(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	saved, err := Save(Credential{Token: "tok-1", Scope: "tenant/acme", ExpiresAt: expires})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved != credentialPath(t) {
		t.Errorf("Save 返回 %q，期望默认路径 %q", saved, credentialPath(t))
	}

	cred, err := Resolve("", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Token != "tok-1" || cred.Scope != "tenant/acme" {
		t.Errorf("解析结果 = %+v", cred)
	}
	if !cred.ExpiresAt.Equal(expires) {
		t.Errorf("过期时间 = %v，期望 %v", cred.ExpiresAt, expires)
	}
	if !strings.HasPrefix(cred.Source, "file:") {
		t.Errorf("来源 = %q，期望以 file: 开头", cred.Source)
	}
	if strings.Contains(cred.Source, cred.Token) {
		t.Errorf("来源标识 %q 里出现了凭证原文", cred.Source)
	}
}

func TestSaveRestrictsPermissions(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	if _, err := Save(Credential{Token: "tok-1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(credentialPath(t))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("凭证文件权限 = %#o，期望 0600", mode)
	}

	dir, err := os.Stat(filepath.Dir(credentialPath(t)))
	if err != nil {
		t.Fatalf("Stat 目录: %v", err)
	}
	if mode := dir.Mode().Perm(); mode != 0o700 {
		t.Errorf("凭证目录权限 = %#o，期望 0700", mode)
	}
}

// 重写凭证要么完整成功、要么保持原值：不能留下半截内容，
// 也不能在目录里留下名字不显眼、内容却是真凭证的临时文件。
func TestSaveReplacesAtomically(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	if _, err := Save(Credential{Token: "old"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Save(Credential{Token: "new", Scope: "tenant/acme"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cred, err := Resolve("", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Token != "new" {
		t.Errorf("令牌 = %q，期望被完整替换为 new", cred.Token)
	}

	entries, err := os.ReadDir(filepath.Dir(credentialPath(t)))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(credentialPath(t)) {
			t.Errorf("目录里残留了 %q——临时文件必须被清理", e.Name())
		}
	}
}

// 写入失败时原凭证必须保持可用，而不是被截断成半截令牌。
func TestSaveFailureKeepsOriginal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时文件权限不生效，无法构造写入失败")
	}
	clearEnv(t)
	isolateHome(t)

	if _, err := Save(Credential{Token: "keep-me"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := filepath.Dir(credentialPath(t))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := Save(Credential{Token: "newer"}); err == nil {
		t.Fatal("目录不可写时 Save 应当失败")
	}

	cred, err := Resolve("", "")
	if err != nil {
		t.Fatalf("写入失败后原凭证应当仍可用: %v", err)
	}
	if cred.Token != "keep-me" {
		t.Errorf("令牌 = %q，期望原值被保留", cred.Token)
	}
}

func TestInsecureFileRejected(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	if _, err := Save(Credential{Token: "tok-1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Chmod(credentialPath(t), 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	_, err := Resolve("", "")
	if !errors.Is(err, ErrCredentialFileInsecure) {
		t.Fatalf("err = %v，期望 ErrCredentialFileInsecure", err)
	}
	// 错误信息要给足线索：权限过宽的提示必须说明期望权限，否则用户不知道该改成什么。
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("错误信息 %q 没有说明期望权限", err)
	}
}

func TestMissingFileIsNoCredential(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	_, err := Resolve("", "")
	if !errors.Is(err, ErrNoCredential) {
		t.Errorf("err = %v，期望 ErrNoCredential", err)
	}
}

func TestSourcePrecedence(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	if _, err := Save(Credential{Token: "from-file", Scope: "tenant/file"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cases := []struct {
		name      string
		flagToken string
		envToken  string
		want      string
	}{
		{"凭证文件兜底", "", "", "from-file"},
		{"环境变量优先于文件", "", "from-env", "from-env"},
		{"命令行参数优先于环境变量", "from-flag", "from-env", "from-flag"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			if tc.envToken != "" {
				t.Setenv(EnvToken, tc.envToken)
			}

			cred, err := Resolve(tc.flagToken, "")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if cred.Token != tc.want {
				t.Errorf("令牌 = %q，期望 %q", cred.Token, tc.want)
			}
		})
	}
}

func TestExplicitFlagScopeOverridesCredentialScope(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	if _, err := Save(Credential{Token: "from-file", Scope: "tenant/file"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cred, err := Resolve("from-flag", "tenant/override")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Scope != "tenant/override" {
		t.Errorf("作用域 = %q，期望被 --scope 覆盖", cred.Scope)
	}
}

func TestExpiryIsLocalOnly(t *testing.T) {
	past := Credential{ExpiresAt: time.Now().Add(-time.Minute)}
	if !past.Expired(time.Now()) {
		t.Error("已过期的凭证应被判为过期")
	}

	future := Credential{ExpiresAt: time.Now().Add(time.Minute)}
	if future.Expired(time.Now()) {
		t.Error("未过期的凭证不应被判为过期")
	}

	// 没有过期时间表示"不因本地判断而过期"，有效性完全交给服务端判定。
	zero := Credential{}
	if zero.Expired(time.Now()) {
		t.Error("未设过期时间的凭证不应被本地判为过期")
	}
}

// 解析失败的错误信息会把文件内容带出去——而文件内容就是凭证。
func TestParseErrorsDoNotLeakToken(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	const secret = "super-secret-token"
	if err := os.MkdirAll(filepath.Dir(credentialPath(t)), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(credentialPath(t), []byte(`{"token": "`+secret+`"`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Resolve("", "")
	if err == nil {
		t.Fatal("损坏的凭证文件应当报错")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("错误信息里出现了凭证原文: %v", err)
	}
}

func TestEmptyTokenIsNoCredential(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	if _, err := Save(Credential{Token: "   "}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := Resolve("", "")
	if !errors.Is(err, ErrNoCredential) {
		t.Errorf("err = %v，期望 ErrNoCredential", err)
	}
}

func TestInvalidExpiresAtRejected(t *testing.T) {
	clearEnv(t)
	isolateHome(t)

	if err := os.MkdirAll(filepath.Dir(credentialPath(t)), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := `{"token": "tok-1", "expires_at": "明天"}`
	if err := os.WriteFile(credentialPath(t), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Resolve("", ""); err == nil {
		t.Error("非 RFC3339 的 expires_at 应当报错")
	}
}
