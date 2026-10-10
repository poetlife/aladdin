import { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Alert, Button, Card, Divider, Form, Input, Space, Typography } from 'antd'
import { KeyRound } from 'lucide-react'

import * as identityApi from '../api/identity'
import { captureTrace, traceIdForAction, type TraceCapture } from '../api/call-trace'
import { messageOf } from '../api/errors'
import { GithubMark, GoogleMark, useSession } from '../auth'
import { RegistrationMode } from '../gen/proto/aladdin/identity/v1/registration_pb'
import { Action, Result, Surface } from '../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { startTimer, type ActionTimer } from '../telemetry/track'
import { BrandMark } from '../ui/BrandMark'

interface LoginFormValues {
  token: string
}

/**
 * 登录页。
 *
 * 它只负责把凭证交给会话层；登录成功后的权限码拉取由 SessionProvider 统一完成，
 * 页面不参与权限计算。
 *
 * **渲染哪些登录入口由服务端决定**：页面先问一次"启用了哪些登录方式"，
 * 按返回的清单渲染。未启用的渠道不在清单里，也就不渲染入口——不是渲染了再
 * 报错，后者会把一次配置缺失表现成一次功能故障（见
 * docs/design/identity/channel-login.md）。
 *
 * **渠道之间的差别只是跳向哪个起点端点**：两个渠道都是重定向型，浏览器导航
 * 到服务端的起点端点后由服务端把整条流程走完（见
 * docs/design/identity/google-login.md）。因此页面里没有任何渠道脚本、
 * 也没有"把渠道凭证交给服务端"的分支。
 */
export function LoginPage(): React.ReactNode {
  const { signIn, status } = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  // null 表示"还没问到"，空数组表示"问到了，没有任何渠道入口"。两者不能混：
  // 前者不该渲染入口，后者同样不该，但只有后者能说明"这是配置结果"。
  const [options, setOptions] = useState<identityApi.GetAuthMethodsResponse | null>(null)
  const methods = options?.methods ?? null

  const from = (location.state as { from?: string } | null)?.from ?? '/'

  useEffect(() => {
    let cancelled = false
    void identityApi
      .getAuthOptions()
      .then((resp) => {
        if (!cancelled) {
          setOptions(resp)
        }
      })
      .catch(() => {
        // 问不到就不渲染任何渠道入口，页面仍可用令牌登录。
        // 登录方式查询失败不该让整个登录页不可用。
        if (!cancelled) {
          setOptions(null)
        }
      })
    return () => {
      cancelled = true
    }
  }, [])

  async function handleSubmit(values: LoginFormValues): Promise<void> {
    setSubmitting(true)
    setError(null)
    const trace = captureTrace()
    // 起点在发起之前、终点是回调返回：这一条事件的耗时就是"等这一次登录的结果"
    // 等了多久，成功与失败在同一口径上（失败的往往还更长）。
    const timer = startTimer()
    try {
      await signIn(values.token, trace)
      trackLogin(Result.OK, 'password', trace, timer)
      void navigate(from, { replace: true })
    } catch (err) {
      trackLogin(Result.FAIL, 'password', trace, timer, err)
      setError(messageOf(err))
    } finally {
      setSubmitting(false)
    }
  }

  const google = methods?.find((m) => m.source === identityApi.AuthSource.Google)
  const github = methods?.find((m) => m.source === identityApi.AuthSource.Github)
  const hasChannelEntry = google !== undefined || github !== undefined

  return (
    // 卡片宽度是弹性的：桌面端维持 420，手机上占满减去左右 16px 的整宽。
    // 顶部间距随视口高度收缩（clamp 的上限就是原来的 96），矮屏的横屏手机
    // 才不会一进来只看见留白。
    <div style={{ display: 'flex', justifyContent: 'center', padding: 'clamp(32px, 12vh, 96px) 16px 24px' }}>
      <Card style={{ width: '100%', maxWidth: 420, minWidth: 0 }}>
        <Space size={8} align="center" style={{ marginBottom: 12 }}>
          <BrandMark size={24} />
          <Typography.Title level={4} style={{ margin: 0 }}>
            阿拉丁神灯
          </Typography.Title>
        </Space>
        <Typography.Paragraph type="secondary">
          权限判定发生在服务端。本界面只根据服务端返回的权限码集合做展示裁剪。
        </Typography.Paragraph>
        {error !== null && <Alert type="error" title={error} style={{ marginBottom: 16 }} />}

        {google !== undefined && (
          // 与 GitHub 分支同构：整页跳转，不是一次 RPC。浏览器导航到服务端的
          // 起点端点，换码与校验都在服务端完成，页面不经手任何渠道凭证。
          <Button
            href="/auth/google/start"
            block
            size="large"
            icon={<GoogleMark size={18} />}
            style={{ marginBottom: 8 }}
          >
            使用 Google 登录
          </Button>
        )}

        {github !== undefined && (
          // 整页跳转，不是一次 RPC：GitHub 的授权码必须由服务端用客户端密钥
          // 换取，因此这条路绕不开浏览器导航。
          <Button
            href="/auth/github/start"
            block
            size="large"
            icon={<GithubMark size={18} />}
            style={{ marginBottom: 8 }}
          >
            使用 GitHub 登录
          </Button>
        )}

        {hasChannelEntry && (
          <>
            <Divider plain>
              <Typography.Text type="secondary">或</Typography.Text>
            </Divider>
            <RegistrationNote mode={options?.registrationMode} />
            <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
              第一次用某个渠道登录会登记一个<Typography.Text strong>新账号</Typography.Text>
              ——不是「我原来那个账号」。它默认没有任何权限（除非管理员给新账号设了默认角色）。把多个渠道归到同一个账号是「绑定」：先登录已有账号，在个人资料里绑定新渠道；即使已经先单独登录过，也可以在那里把它并入已有账号。
            </Typography.Paragraph>
          </>
        )}

        <Form<LoginFormValues> layout="vertical" onFinish={(values) => void handleSubmit(values)}>
          <Form.Item
            name="token"
            label="访问凭证"
            rules={[{ required: true, message: '请输入访问凭证' }]}
          >
            <Input.Password
              prefix={<KeyRound size={14} />}
              placeholder="机器凭证或访问令牌"
              autoComplete="off"
            />
          </Form.Item>
          <Button type="primary" htmlType="submit" block size="large" loading={submitting || status === 'loading'}>
            登录
          </Button>
        </Form>
      </Card>
    </div>
  )
}

/**
 * 站点的准入姿态说明。
 *
 * **只在服务端明确说了姿态时才写那句话。** 读不到策略（UNSPECIFIED）时什么都不说
 * ——把一次读取失败说成"这里开放注册"或"这里需要邀请码"，是在替服务端做一个它
 * 当时答不上来的断言。任何一种姿态下的真正放行都由服务端在登记那一刻决定，
 * 因此这里说不说都不改变结果，只改变使用者有没有被提前告知。
 */
function RegistrationNote({ mode }: { mode: RegistrationMode | undefined }): React.ReactNode {
  if (mode === RegistrationMode.INVITE) {
    return (
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        本站需要<Typography.Text strong>邀请码</Typography.Text>
        ：第一次用某个渠道登录时，需要填入管理员发给你的邀请码。已经在册的账号不受影响。
      </Typography.Paragraph>
    )
  }
  if (mode === RegistrationMode.CLOSED) {
    return (
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        本站<Typography.Text strong>不接受新账号</Typography.Text>
        ，只有已经在册的账号可以登录。需要账号请联系管理员。
      </Typography.Paragraph>
    )
  }
  return null
}

/**
 * 上报一次登录尝试。
 *
 * 登录的 RPC 本身已有服务端请求留痕，但它记不到**渠道**（请求体不进日志，
 * 而渠道在请求体里）。"哪个渠道的失败在涨"正是这里要回答的问题，因此这条事件
 * 与请求留痕不是重复：它补的是请求留痕缺失的那一维。
 *
 * `trace` 是这次登录调用的链路标识捕获：**成功与失败都要带**，否则这条事件与
 * 它对应的那次请求只能靠时间戳对账（见 api/call-trace）。
 *
 * `timer` 量的是"点了登录到拿到结果"的那一段。**整页重定向的那两个渠道不走这里**
 * ——它们在回调页才有结果，起止跨了页面，本次不覆盖（见 docs/observability.md）。
 */
function trackLogin(
  result: Result,
  channel: string,
  trace: TraceCapture,
  timer: ActionTimer,
  error?: unknown,
): void {
  timer.end({
    surface: Surface.WEB_AUTH,
    action: Action.AUTH_LOGIN,
    result,
    traceId: traceIdForAction(trace, error),
    attrs: { channel },
  })
}
