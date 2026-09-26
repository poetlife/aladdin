package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/poetlife/aladdin/internal/rbac"
)

// 会话链路的语义用例。
//
// 时钟由用例推进，不用 sleep：签发、过期、回收都是"到点"的判定，靠等待
// 来触发会让用例慢且不稳定，而慢与不稳定都会让人后来把它删掉。
//
// 这些用例只跑内存实现。SQL 实现与它跑的是**同一套存储契约**
// （见 gormstore 的 store_contract_test.go），另外单独验证它在
// 重开库之后仍然认得之前签发的会话。

var testBaseTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// clock 是一份可推进的假时钟。
type clock struct {
	at time.Time
}

func (c *clock) now() time.Time { return c.at }

func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

// newTestSessions 造一个挂在假时钟上的会话入口。
func newTestSessions() (*Sessions, *clock) {
	c := &clock{at: testBaseTime}
	sessions := NewSessions(NewMemoryStore())
	sessions.now = c.now
	return sessions, c
}

func testSubject() rbac.Subject {
	return rbac.Subject{
		ID:           "google:109876543210987654321",
		Type:         rbac.SubjectTypeUser,
		DefaultScope: "tenant/acme",
	}
}

// 签发即生效：刚签发的凭证可以立即用于认证，且还原出同一个主体。
func TestIssueThenVerify(t *testing.T) {
	sessions, _ := newTestSessions()
	ctx := context.Background()

	issued, err := sessions.Issue(ctx, testSubject())
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("签发的凭证为空")
	}

	got, err := sessions.Verify(ctx, issued.Token)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if got != testSubject() {
		t.Errorf("主体 = %+v，期望 %+v", got, testSubject())
	}
}

// 每次签发的凭证都不同：凭证是密码学随机数，不是可推导的值。
func TestIssueProducesDistinctTokens(t *testing.T) {
	sessions, _ := newTestSessions()
	ctx := context.Background()

	seen := map[string]bool{}
	for range 8 {
		issued, err := sessions.Issue(ctx, testSubject())
		if err != nil {
			t.Fatalf("签发失败: %v", err)
		}
		if seen[issued.Token] {
			t.Fatalf("签发了重复的凭证: %s", issued.Token)
		}
		seen[issued.Token] = true
	}
}

// 未签发过的凭证一律不成立。
func TestVerifyRejectsUnknownToken(t *testing.T) {
	sessions, _ := newTestSessions()
	if _, err := sessions.Verify(context.Background(), "从未签发过的凭证"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v，期望 ErrSessionNotFound", err)
	}
	if _, err := sessions.Verify(context.Background(), ""); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("空凭证 err = %v，期望 ErrSessionNotFound", err)
	}
}

// 到点即失效，且在**恰好到点**那一刻就失效——不留"还差一毫秒"的缝。
func TestVerifyRejectsExpiredToken(t *testing.T) {
	sessions, c := newTestSessions()
	ctx := context.Background()

	issued, err := sessions.Issue(ctx, testSubject())
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	c.advance(DefaultSessionTTL - time.Nanosecond)
	if _, err := sessions.Verify(ctx, issued.Token); err != nil {
		t.Fatalf("未到期的凭证被拒: %v", err)
	}

	c.advance(time.Nanosecond)
	if _, err := sessions.Verify(ctx, issued.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("到点的凭证 err = %v，期望 ErrSessionNotFound", err)
	}
}

// 撤销立即生效，且是幂等的。
func TestRevokeIsImmediateAndIdempotent(t *testing.T) {
	sessions, _ := newTestSessions()
	ctx := context.Background()

	issued, err := sessions.Issue(ctx, testSubject())
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	for range 2 {
		if err := sessions.Revoke(ctx, issued.Token); err != nil {
			t.Fatalf("撤销失败: %v", err)
		}
	}
	if _, err := sessions.Verify(ctx, issued.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("撤销后 err = %v，期望 ErrSessionNotFound", err)
	}

	// 撤销一份从未签发过的凭证同样不报错：重复登出与登出一个早已过期的
	// 会话，结果一致。
	if err := sessions.Revoke(ctx, "从未签发过的凭证"); err != nil {
		t.Errorf("撤销未知凭证 err = %v，期望 nil", err)
	}
}

// 刷新是续期，不是再发一个：新凭证可用，旧凭证同时失效。
func TestRefreshInvalidatesOldToken(t *testing.T) {
	sessions, _ := newTestSessions()
	ctx := context.Background()

	old, err := sessions.Issue(ctx, testSubject())
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	fresh, err := sessions.Refresh(ctx, old.Token)
	if err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if fresh.Token == old.Token {
		t.Fatal("刷新返回了同一份凭证")
	}

	if _, err := sessions.Verify(ctx, old.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("旧凭证 err = %v，期望 ErrSessionNotFound", err)
	}
	if _, err := sessions.Verify(ctx, fresh.Token); err != nil {
		t.Errorf("新凭证 err = %v，期望可用", err)
	}
}

// 刷新同样只接受仍然有效的凭证。
func TestRefreshRejectsInvalidToken(t *testing.T) {
	sessions, c := newTestSessions()
	ctx := context.Background()

	issued, err := sessions.Issue(ctx, testSubject())
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	c.advance(DefaultSessionTTL)

	if _, err := sessions.Refresh(ctx, issued.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("过期凭证刷新 err = %v，期望 ErrSessionNotFound", err)
	}
	if _, err := sessions.Refresh(ctx, "从未签发过的凭证"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("未知凭证刷新 err = %v，期望 ErrSessionNotFound", err)
	}
}

// 回收只清掉已过期的行，返回清掉的条数。
func TestCleanupRemovesOnlyExpired(t *testing.T) {
	sessions, c := newTestSessions()
	ctx := context.Background()

	expiring, err := sessions.Issue(ctx, testSubject())
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	c.advance(DefaultSessionTTL / 2)

	survivor, err := sessions.Issue(ctx, testSubject())
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	c.advance(DefaultSessionTTL/2 + time.Nanosecond)

	removed, err := sessions.Cleanup(ctx)
	if err != nil {
		t.Fatalf("回收失败: %v", err)
	}
	if removed != 1 {
		t.Errorf("回收条数 = %d，期望 1", removed)
	}
	if _, err := sessions.Verify(ctx, expiring.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("被回收的凭证 err = %v，期望 ErrSessionNotFound", err)
	}
	if _, err := sessions.Verify(ctx, survivor.Token); err != nil {
		t.Errorf("未过期的凭证 err = %v，期望可用", err)
	}
}

// 存储不可用时必须报错，**不能**退化成"这份凭证不成立"。
//
// 混为一谈的后果是把一次数据库故障表现成一次凭证问题：客户端会引导用户
// 重新登录，而重新登录同样失败，于是"所有人都登不上了"被当成认证配置错误
// 去排查。
func TestStoreUnavailableIsNotCredentialFailure(t *testing.T) {
	sessions := NewSessions(failingStore{})
	ctx := context.Background()

	_, err := sessions.Verify(ctx, "任意凭证")
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("err = %v，期望 ErrStoreUnavailable", err)
	}
	if errors.Is(err, ErrSessionNotFound) {
		t.Error("存储故障被当成了凭证失效")
	}
	if _, err := sessions.Issue(ctx, testSubject()); !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("签发 err = %v，期望 ErrStoreUnavailable", err)
	}
}

// failingStore 是一个永远失败的会话存储，用来验证错误类别不会被吞掉。
type failingStore struct{}

var errBoom = errors.New("库炸了")

func (failingStore) Put(context.Context, string, Session) error { return errBoom }

func (failingStore) Get(context.Context, string) (Session, error) { return Session{}, errBoom }

func (failingStore) Delete(context.Context, string) error { return errBoom }

func (failingStore) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, errBoom }

// HashToken 是凭证与库里那一行之间唯一的换算入口：同一份凭证永远得到
// 同一个键，不同的凭证永远得到不同的键。
func TestHashTokenIsDeterministic(t *testing.T) {
	a := HashToken("凭证甲")
	if a != HashToken("凭证甲") {
		t.Error("同一份凭证两次摘要不同")
	}
	if a == HashToken("凭证乙") {
		t.Error("不同的凭证得到了同一个摘要")
	}
	// 定长十六进制：库里那一列的长度按它定死（见 internal/database/schema.go）。
	if len(a) != 64 {
		t.Errorf("摘要长度 = %d，期望 64", len(a))
	}
}
