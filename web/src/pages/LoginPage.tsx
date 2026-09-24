import { useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Alert, Button, Card, Form, Input, Typography } from 'antd'

import { useSession } from '../auth'

interface LoginFormValues {
  token: string
}

/**
 * 登录页。
 *
 * 它只负责把凭证交给会话层；登录成功后的权限码拉取由 SessionProvider 统一完成，
 * 页面不参与权限计算。
 */
export function LoginPage(): React.ReactNode {
  const { signIn, status } = useSession()
  const navigate = useNavigate()
  const location = useLocation()
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const from = (location.state as { from?: string } | null)?.from ?? '/'

  async function handleSubmit(values: LoginFormValues): Promise<void> {
    setSubmitting(true)
    setError(null)
    try {
      await signIn(values.token)
      void navigate(from, { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 96 }}>
      <Card style={{ width: 420 }}>
        <Typography.Title level={4}>阿拉丁神灯</Typography.Title>
        <Typography.Paragraph type="secondary">
          权限判定发生在服务端。本界面只根据服务端返回的权限码集合做展示裁剪。
        </Typography.Paragraph>
        {error !== null && <Alert type="error" message={error} style={{ marginBottom: 16 }} />}
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
