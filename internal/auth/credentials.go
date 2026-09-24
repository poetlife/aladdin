// Package auth 负责 CLI 侧的凭证解析。
//
// 它只解析与校验凭证的**存在性与明显过期**，不做任何权限判断，
// 也不缓存权限判定结果（见 docs/design/rbac/cli-permissions.md）。
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 环境变量名。与 config 包的 ALADDIN_ 前缀保持一致。
const (
	EnvToken = "ALADDIN_TOKEN"
	EnvScope = "ALADDIN_SCOPE"
)

var (
	// ErrNoCredential 表示三个来源都没有找到凭证。
	ErrNoCredential = errors.New("未找到凭证")
	// ErrCredentialFileInsecure 表示凭证文件权限过宽。
	ErrCredentialFileInsecure = errors.New("凭证文件权限过宽")
)

// Credential 是解析出的凭证。
type Credential struct {
	Token     string
	Scope     string
	ExpiresAt time.Time
	// Source 记录凭证来自哪个来源，用于 --debug 输出与排障。
	Source string
}

// Expired 报告凭证是否已明显过期。
//
// 这是 CLI 唯一允许的本地判断：它只读凭证自身的过期时间字段，
// 不涉及任何权限信息。
func (c Credential) Expired(now time.Time) bool {
	return !c.ExpiresAt.IsZero() && now.After(c.ExpiresAt)
}

// fileCredential 是凭证文件的格式。
type fileCredential struct {
	Token     string `json:"token"`
	Scope     string `json:"scope"`
	ExpiresAt string `json:"expires_at"`
}

// DefaultPath 返回凭证文件的默认路径。
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aladdin", "credentials.json"), nil
}

// Resolve 按固定优先级解析凭证：显式参数 > 环境变量 > 凭证文件。
//
// 先命中者胜出；三个来源都没有时返回 ErrNoCredential，
// 调用方应据此提示用户执行登录，而**不是**提示"权限不足"。
func Resolve(flagToken, flagScope string) (Credential, error) {
	if flagToken != "" {
		return Credential{Token: flagToken, Scope: flagScope, Source: "flag"}, nil
	}
	if token := os.Getenv(EnvToken); token != "" {
		return Credential{Token: token, Scope: scopeOrDefault(flagScope, os.Getenv(EnvScope)), Source: "env"}, nil
	}
	cred, err := fromFile()
	if err != nil {
		return Credential{}, err
	}
	if flagScope != "" {
		cred.Scope = flagScope
	}
	return cred, nil
}

func scopeOrDefault(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func fromFile() (Credential, error) {
	path, err := DefaultPath()
	if err != nil {
		return Credential{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credential{}, ErrNoCredential
		}
		return Credential{}, err
	}

	// 凭证文件必须只有属主可读。权限过宽时拒绝使用并提示，
	// 而不是"警告后继续"——警告在实践中一定会被忽略。
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return Credential{}, fmt.Errorf("%w: %s 权限为 %#o，应改为 0600", ErrCredentialFileInsecure, path, mode)
	}

	// path 由 DefaultPath 从 os.UserConfigDir 推导，不接受外部输入。
	raw, err := os.ReadFile(path) //nolint:gosec // 路径来源受控，非用户输入
	if err != nil {
		return Credential{}, err
	}
	var fc fileCredential
	if err := json.Unmarshal(raw, &fc); err != nil {
		return Credential{}, fmt.Errorf("解析凭证文件 %s 失败: %w", path, err)
	}
	if strings.TrimSpace(fc.Token) == "" {
		return Credential{}, ErrNoCredential
	}

	cred := Credential{Token: fc.Token, Scope: fc.Scope, Source: "file:" + path}
	if fc.ExpiresAt != "" {
		ts, err := time.Parse(time.RFC3339, fc.ExpiresAt)
		if err != nil {
			return Credential{}, fmt.Errorf("凭证文件 %s 的 expires_at 不是 RFC3339: %w", path, err)
		}
		cred.ExpiresAt = ts
	}
	return cred, nil
}

// Save 把凭证写入默认路径，权限为 0600。
//
// 先在同目录写一个 0600 的临时文件，成功后 rename 到目标路径。这样做的两个理由：
//
//   - **要么完整成功、要么保持原值**。直接截断目标文件再写，中途失败
//     （磁盘满、进程被杀）会留下内容截断的凭证文件——它表现为"未认证"，
//     用户无法从提示中看出真正原因是磁盘问题。
//   - **创建即受限**。临时文件由 os.CreateTemp 以 0600 创建，不存在
//     "先建好再收紧权限"的宽权限窗口，也不需要先写后 chmod。
func Save(cred Credential) (string, error) {
	path, err := DefaultPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	payload := fileCredential{Token: cred.Token, Scope: cred.Scope}
	if !cred.ExpiresAt.IsZero() {
		payload.ExpiresAt = cred.ExpiresAt.Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}

	// 临时文件必须与目标同目录：rename 只在同一文件系统内才是原子的。
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()

	// 任何一步失败都要清掉临时文件，否则用户配置目录里会留下一个名字不显眼、
	// 内容却是真凭证的残留。
	if err := writeAndClose(tmp, raw); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return path, nil
}

// writeAndClose 写入并关闭。
//
// Close 的错误必须上报：缓冲区可能要到关闭时才真正落盘。
func writeAndClose(f *os.File, raw []byte) error {
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
