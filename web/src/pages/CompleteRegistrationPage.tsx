import { useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { Alert, Button, Card, Form, Input, Space, Typography } from 'antd'
import { Ticket } from 'lucide-react'

import * as registrationApi from '../api/registration'
import { messageOf } from '../api/errors'
import { useSession } from '../auth'
import { BrandMark } from '../ui/BrandMark'

interface RegistrationFormValues {
  code: string
}

/** 邀请码的提示形状，与服务端签发时的分组一致（每 4 个字符一组）。 */
const CODE_PLACEHOLDER = 'XXXX-XXXX-XXXX-XXXX'

/**
 * 填邀请码那一页。
 *
 * 它是**公开**的：走到这里的人还没有账号，因此它不能挂在 `RequirePermission`
 * 之下。地址由服务端在回调里给出（见 internal/server 的 RegistrationPath），
 * 改这里的路径要同时改那里。
 *
 * **凭据不在这个页面上。** 服务端在回调时把已经校验过的渠道身份记成一份一次性
 * 凭据，只经 HttpOnly cookie 交回；本页提交的**只有邀请码**——没有主体、也没有
 * 渠道身份。因此这里有且只有一个输入框。
 *
 * 码不做前端折算：大小写、连字符、易混字符都由服务端那一处归一（见
 * docs/ssot-registry.md）。前端再折一遍就是第二份实现，而漂移的表现是
 * "管理员发的码明明是对的，这一页却说无效"。
 */
export function CompleteRegistrationPage(): React.ReactNode {
  const { adoptSessionToken, status, subject } = useSession()
  const navigate = useNavigate()
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  // 已经有会话的人不该停在这一页：他可能是点了后退回来的，而这一页的提交会把
  // 他登进一个**新账号**——那不是他要的。
  if (status === 'authenticated' && subject !== null) {
    return <Navigate to="/" replace />
  }

  async function handleSubmit(values: RegistrationFormValues): Promise<void> {
    setSubmitting(true)
    setError(null)
    try {
      const resp = await registrationApi.completeRegistration(values.code.trim())
      // 会话凭证与渠道登录一样交给会话层；权限码集合由 SessionProvider 统一拉取，
      // 这一页不参与任何权限计算。
      await adoptSessionToken(resp.accessToken)
      void navigate('/', { replace: true })
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'center', padding: 'clamp(32px, 12vh, 96px) 16px 24px' }}>
      <Card style={{ width: '100%', maxWidth: 420, minWidth: 0 }}>
        <Space size={8} align="center" style={{ marginBottom: 12 }}>
          <BrandMark size={24} />
          <Typography.Title level={4} style={{ margin: 0 }}>
            完成注册
          </Typography.Title>
        </Space>
        <Typography.Paragraph type="secondary">
          你的登录渠道已经确认过了，只差一步：填入管理员发给你的邀请码。
        </Typography.Paragraph>
        {error !== null && <Alert type="error" title={error} style={{ marginBottom: 16 }} />}

        <Form<RegistrationFormValues> layout="vertical" onFinish={(values) => void handleSubmit(values)}>
          <Form.Item name="code" label="邀请码" rules={[{ required: true, message: '请输入邀请码' }]}>
            <Input
              prefix={<Ticket size={14} />}
              placeholder={CODE_PLACEHOLDER}
              autoComplete="off"
              autoFocus
              size="large"
            />
          </Form.Item>
          <Button type="primary" htmlType="submit" block size="large" loading={submitting}>
            完成注册
          </Button>
        </Form>

        <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginTop: 16, marginBottom: 0 }}>
          大小写与连字符都不影响。<a href="/login">重新登录</a>
        </Typography.Paragraph>
      </Card>
    </div>
  )
}
