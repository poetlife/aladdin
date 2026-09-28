package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/auth"
	"github.com/poetlife/aladdin/pkg/client"
)

// deviceLoginIntervalFallback 是服务端没有给出可用轮询间隔时的兜底。
const deviceLoginIntervalFallback = 5 * time.Second

// runDeviceLogin 走一次设备码登录：发起、把短码打给人看、轮询、落盘。
//
// 它拿到的是**使用者本人的会话**，而不是一份共享的机器凭证——批准发生在
// 浏览器里已经登录的那个账号上（见 docs/design/identity/device-login.md）。
func runDeviceLogin(cmd *cobra.Command) error {
	cfg, err := resolvedConfig()
	if err != nil {
		return err
	}
	if err := ensureTelemetry(cfg); err != nil {
		return err
	}

	// 设备码登录的两个接口都是公开方法，因此这条连接不带任何凭证。
	c, err := client.Dial(client.Options{Address: cfg.Address, Timeout: cfg.Timeout})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	identity := identityv1.NewIdentityServiceClient(c.Conn())

	start, err := startDeviceLogin(c, identity)
	if err != nil {
		return err
	}
	// 有效期只解析一次，提示与轮询共用：两边各解析一遍，就会出现"提示里说的
	// 截止时间"与"轮询实际用的截止时间"漂移的可能。
	expiresAt, err := time.Parse(time.RFC3339, start.GetExpiresAt())
	if err != nil {
		return fmt.Errorf("服务端返回的 expires_at 不是 RFC3339: %w", err)
	}
	printDeviceLoginPrompt(cmd, start, expiresAt)

	credential, err := awaitDeviceLogin(c, identity, start, expiresAt)
	if err != nil {
		return err
	}

	// 凭证文件只有一份：这次登录会覆盖本机原来那份（可能是机器凭证）。
	// 覆盖是预期行为，但使用者该知道原来那份已经不在了。
	replaced := credentialFileExists()

	path, err := auth.Save(credential)
	if err != nil {
		return err
	}

	if flags.output == "json" {
		return printJSON(map[string]string{
			"credential_file": path,
			"expires_at":      credential.ExpiresAt.Format(time.RFC3339),
		})
	}
	printf(cmd.OutOrStdout(), "已登录，凭证已保存到 %s（过期时间 %s）\n",
		path, credential.ExpiresAt.Format(time.RFC3339))
	if replaced {
		printf(cmd.OutOrStdout(), "注意：本机此前的那份凭证已被这次登录替换\n")
	}
	return nil
}

// startDeviceLogin 发起一次设备码登录。
func startDeviceLogin(c *client.Client, identity identityv1.IdentityServiceClient) (*identityv1.StartDeviceLoginResponse, error) {
	ctx, cancel := c.Context()
	defer cancel()

	resp, err := identity.StartDeviceLogin(ctx, &identityv1.StartDeviceLoginRequest{})
	if err != nil {
		return nil, err
	}
	if resp.GetDeviceCode() == "" || resp.GetUserCode() == "" {
		return nil, fmt.Errorf("服务端没有给出设备码或短码")
	}
	if resp.GetVerificationUri() == "" {
		return nil, fmt.Errorf("服务端没有给出批准页地址")
	}
	return resp, nil
}

// printDeviceLoginPrompt 把短码、地址与有效期显著地打出来。
//
// 短码要由人**手动输入**到浏览器里，而不是点一个带着码的链接：终端上显示它
// 的意义就在于人会去核对"页面上说的这次请求，是不是我刚发起的这一次"。把码
// 嵌进地址会消掉这次核对，而那正是挡住"被登进别人账号"的唯一防线（见
// docs/design/identity/device-login.md）。
//
// 有效期必须说出来：不说，人就会对着一个早就失效的短码反复输入。
//
// 它写 **stderr**：这是给人看的提示，不是命令的结果（见 docs/observability.md）。
// 写在 stdout 上会污染 --output json，也会把与口令同级的短码送进脚本的管道。
func printDeviceLoginPrompt(cmd *cobra.Command, start *identityv1.StartDeviceLoginResponse, expiresAt time.Time) {
	out := cmd.ErrOrStderr()
	printf(out, "\n  在浏览器打开：%s\n", start.GetVerificationUri())
	printf(out, "  输入代码：%s\n\n", start.GetUserCode())
	printf(out, "这个代码在 %s 之前有效（本机时间），过期后重新执行 aladdin login。\n",
		expiresAt.Local().Format("2006-01-02 15:04"))
	printf(out, "只有当你刚刚在这台机器上发起登录时才继续。等待批准…\n")
}

// awaitDeviceLogin 按服务端给出的间隔轮询，直到批准、拒绝或过期。
func awaitDeviceLogin(c *client.Client, identity identityv1.IdentityServiceClient, start *identityv1.StartDeviceLoginResponse, expiresAt time.Time) (auth.Credential, error) {
	interval := deviceLoginIntervalFallback
	if seconds := start.GetIntervalSeconds(); seconds > 0 {
		interval = time.Duration(seconds) * time.Second
	}

	for {
		if !time.Now().Before(expiresAt) {
			return auth.Credential{}, deviceLoginFailed("这次登录已经过期，请重新执行 aladdin login")
		}

		// 先等一个间隔再问：刚发起就立刻问一次只会拿到"待批准"，而人在浏览器
		// 里的动作总归要花时间。
		time.Sleep(interval)

		ctx, cancel := c.Context()
		resp, err := identity.PollDeviceLogin(ctx, &identityv1.PollDeviceLoginRequest{
			DeviceCode: start.GetDeviceCode(),
		})
		cancel()
		if err != nil {
			return auth.Credential{}, err
		}

		switch resp.GetState() {
		case identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_APPROVED:
			return credentialFromPoll(resp)
		case identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_DENIED:
			return auth.Credential{}, deviceLoginFailed("这次登录被拒绝了")
		case identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_EXPIRED:
			return auth.Credential{}, deviceLoginFailed("这次登录已经失效，请重新执行 aladdin login")
		case identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_PENDING:
			// 继续等。
		default:
			return auth.Credential{}, fmt.Errorf("服务端返回了未知的登录状态：%v", resp.GetState())
		}
	}
}

// credentialFromPoll 把交付的凭证折成本地凭证。
func credentialFromPoll(resp *identityv1.PollDeviceLoginResponse) (auth.Credential, error) {
	if resp.GetAccessToken() == "" {
		return auth.Credential{}, fmt.Errorf("服务端批准了这次登录却没有交出凭证")
	}

	// **作用域留空**：会话自带一份冻结的默认作用域，服务端按凭证取用
	// （SCOPE_SOURCE_CREDENTIAL）。在这里写一个作用域只会给出一份可能已经
	// 过时的副本，而它对不上服务端那次判定。
	credential := auth.Credential{Token: resp.GetAccessToken()}
	if resp.GetExpiresAt() != "" {
		ts, err := time.Parse(time.RFC3339, resp.GetExpiresAt())
		if err != nil {
			return auth.Credential{}, fmt.Errorf("服务端返回的 expires_at 不是 RFC3339: %w", err)
		}
		credential.ExpiresAt = ts
	}
	return credential, nil
}

// deviceLoginFailed 把"登录没完成"按未认证退出（退出码 3）。
//
// 3 的含义是"脚本应触发重新登录"，这正是这里的情形。归入未分类失败会让脚本
// 无法区分"登录没成"与"工具坏了"（见 exitcode.go）。
func deviceLoginFailed(message string) error {
	return status.Error(codes.Unauthenticated, message)
}

// credentialFileExists 报告本机是否已经有一份凭证。
//
// 它只用来把"覆盖了原来那份"这件事说明白，不参与任何判定：读不到就当没有。
func credentialFileExists() bool {
	path, err := auth.DefaultPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
