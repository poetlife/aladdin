import { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Alert, Button, Card, Divider, Form, Input, Space, Typography, theme } from 'antd'
import { KeyRound, Lamp, LogIn } from 'lucide-react'

import * as identityApi from '../api/identity'
import { messageOf } from '../api/errors'
import { GoogleSignInButton, useSession } from '../auth'

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
 * 渠道之间的差别只在"怎么拿到凭证"：Google 是浏览器内的登录控件，GitHub 是
 * 一次整页跳转（它必须由服务端用客户端密钥换取令牌，浏览器给不出可用的码）。
 */
export function LoginPage(): React.ReactNode {
  const { signIn, signInWithGoogle, status } = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  const { token } = theme.useToken()
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  // null 表示"还没问到"，空数组表示"问到了，没有任何渠道入口"。两者不能混：
  // 前者不该渲染入口，后者同样不该，但只有后者能说明"这是配置结果"。
  const [methods, setMethods] = useState<identityApi.AuthMethod[] | null>(null)

  const from = (location.state as { from?: string } | null)?.from ?? '/'

  useEffect(() => {
    let cancelled = false
    void identityApi
      .getAuthMethods()
      .then((list) => {
        if (!cancelled) {
          setMethods(list)
        }
      })
      .catch(() => {
        // 问不到就不渲染任何渠道入口，页面仍可用令牌登录。
        // 登录方式查询失败不该让整个登录页不可用。
        if (!cancelled) {
          setMethods([])
        }
      })
    return () => {
      cancelled = true
    }
  }, [])

  async function handleGoogleCredential(idToken: string): Promise<void> {
    setSubmitting(true)
    setError(null)
    try {
      await signInWithGoogle(idToken)
      void navigate(from, { replace: true })
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setSubmitting(false)
    }
  }

  async function handleSubmit(values: LoginFormValues): Promise<void> {
    setSubmitting(true)
    setError(null)
    try {
      await signIn(values.token)
      void navigate(from, { replace: true })
    } catch (err) {
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
          <Lamp size={22} color={token.colorPrimary} />
          <Typography.Title level={4} style={{ margin: 0 }}>
            阿拉丁神灯
          </Typography.Title>
        </Space>
        <Typography.Paragraph type="secondary">
          权限判定发生在服务端。本界面只根据服务端返回的权限码集合做展示裁剪。
        </Typography.Paragraph>
        {error !== null && <Alert type="error" message={error} style={{ marginBottom: 16 }} />}

        {google !== undefined && (
          <div style={{ display: 'flex', justifyContent: 'center', marginBottom: 8 }}>
            <GoogleSignInButton
              clientId={google.clientId}
              onCredential={(idToken) => void handleGoogleCredential(idToken)}
            />
          </div>
        )}

        {github !== undefined && (
          // 整页跳转，不是一次 RPC：GitHub 的授权码必须由服务端用客户端密钥
          // 换取，因此这条路绕不开浏览器导航。
          <Button href="/auth/github/start" block icon={<LogIn size={16} />} style={{ marginBottom: 8 }}>
            使用 GitHub 登录
          </Button>
        )}

        {hasChannelEntry && (
          <>
            <Divider plain>
              <Typography.Text type="secondary">或</Typography.Text>
            </Divider>
            <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
              第一次用某个渠道登录会得到一个<Typography.Text strong>没有权限</Typography.Text>
              的新账号。把多个渠道归到同一个账号是「绑定」：先登录已有账号，在个人资料里绑定新渠道；
              即使已经先单独登录过，也可以在那里把它并入已有账号。
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
          <Button type="primary" htmlType="submit" block loading={submitting || status === 'loading'}>
            登录
          </Button>
        </Form>
      </Card>
    </div>
  )
}
