import { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Alert, Button, Card, Divider, Form, Input, Typography } from 'antd'

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
 * 拿到客户端标识才渲染 Google 按钮。未启用时不渲染入口，而不是渲染了再报错——
 * 后者会把一次配置缺失表现成一次功能故障（见
 * docs/design/identity/google-login.md）。
 */
export function LoginPage(): React.ReactNode {
  const { signIn, signInWithGoogle, status } = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  // null 表示"还没问到"，空串表示"问到了，未启用"。两者不能混：
  // 前者不该渲染入口，后者同样不该，但只有后者能说明"这是配置结果"。
  const [googleClientId, setGoogleClientId] = useState<string | null>(null)

  const from = (location.state as { from?: string } | null)?.from ?? '/'

  useEffect(() => {
    let cancelled = false
    void identityApi
      .getAuthMethods()
      .then((methods) => {
        if (!cancelled) {
          setGoogleClientId(methods.googleClientId)
        }
      })
      .catch(() => {
        // 问不到就不渲染任何可选入口，页面仍可用令牌登录。
        // 登录方式查询失败不该让整个登录页不可用。
        if (!cancelled) {
          setGoogleClientId('')
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

  const googleEnabled = googleClientId !== null && googleClientId !== ''

  return (
    <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 96 }}>
      <Card style={{ width: 420 }}>
        <Typography.Title level={4}>阿拉丁神灯</Typography.Title>
        <Typography.Paragraph type="secondary">
          权限判定发生在服务端。本界面只根据服务端返回的权限码集合做展示裁剪。
        </Typography.Paragraph>
        {error !== null && <Alert type="error" message={error} style={{ marginBottom: 16 }} />}

        {googleEnabled && (
          <>
            <div style={{ display: 'flex', justifyContent: 'center', marginBottom: 8 }}>
              <GoogleSignInButton
                clientId={googleClientId}
                onCredential={(idToken) => void handleGoogleCredential(idToken)}
              />
            </div>
            <Divider plain>
              <Typography.Text type="secondary">或</Typography.Text>
            </Divider>
          </>
        )}

        <Form<LoginFormValues> layout="vertical" onFinish={(values) => void handleSubmit(values)}>
          <Form.Item
            name="token"
            label="访问凭证"
            rules={[{ required: true, message: '请输入访问凭证' }]}
          >
            <Input.Password placeholder="机器凭证或访问令牌" autoComplete="off" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={submitting || status === 'loading'}>
            登录
          </Button>
        </Form>
      </Card>
    </div>
  )
}
