import { useState } from 'react'
import { Alert, Button, Card, Form, Input, Space, Typography, theme } from 'antd'
import { Check, Terminal, X } from 'lucide-react'

import * as identityApi from '../api/identity'
import { messageOf } from '../api/errors'
import { useProfile } from '../profile'

interface DeviceFormValues {
  userCode: string
}

/**
 * 命令行登录的批准页。
 *
 * 它**不参与任何判定**：短码决定"哪一次登录"，当前会话决定"以谁的名义"。
 * 页面唯一的职责是把这件事说清楚、让人做一次明确的决定。
 *
 * 短码要人**手动输入**，而不是从地址里读——终端上显示它的意义就在于人会去
 * 核对"页面上说的这次请求，是不是我刚发起的这一次"，而这次核对正是挡住
 * "被登进别人账号"的唯一防线（见 docs/design/identity/device-login.md）。
 */
export function DeviceApprovalPage(): React.ReactNode {
  const { profile, loading: profileLoading, error: profileError, reload } = useProfile()
  const { token } = theme.useToken()
  const [form] = Form.useForm<DeviceFormValues>()
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [decided, setDecided] = useState<'approved' | 'denied' | null>(null)

  const displayName = profile?.displayName ?? ''
  // 批准意味着"以这个账号的名义"把会话交给终端。账号还没读到（或读失败）时，
  // 人做不了这次核对，因此"批准"保持不可用——这不是装饰，见
  // docs/design/identity/device-login.md 的"批准页必须先显示你正在以哪个账号批准"。
  // "拒绝"是安全的那一侧，任何时候都能按：读不到账号不该反过来挡住唯一能挡住
  // 这次登录的动作。
  const identityKnown = profile !== null

  async function decide(approve: boolean): Promise<void> {
    const userCode = form.getFieldValue('userCode') ?? ''
    setSubmitting(true)
    setError(null)
    try {
      if (approve) {
        await identityApi.approveDeviceLogin(userCode)
        setDecided('approved')
      } else {
        await identityApi.denyDeviceLogin(userCode)
        setDecided('denied')
      }
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setSubmitting(false)
    }
  }

  if (decided !== null) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 96 }}>
        <Card style={{ width: 440 }}>
          <Alert
            type={decided === 'approved' ? 'success' : 'info'}
            message={decided === 'approved' ? '已批准' : '已拒绝'}
            description={
              decided === 'approved'
                ? '可以回到终端继续了。这个代码已经失效。'
                : '终端上的这次登录不会完成，可以在那里重新发起。'
            }
          />
        </Card>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 96 }}>
      <Card style={{ width: 440 }}>
        <Space size={8} align="center" style={{ marginBottom: 12 }}>
          <Terminal size={22} color={token.colorPrimary} />
          <Typography.Title level={4} style={{ margin: 0 }}>
            授权命令行登录
          </Typography.Title>
        </Space>

        <Typography.Paragraph type="secondary">
          在终端执行 <Typography.Text code>aladdin login</Typography.Text>{' '}
          会打印一个代码，把它输入到这里。
        </Typography.Paragraph>

        <Typography.Paragraph>
          当前账号：
          <Typography.Text strong>
            {displayName !== '' ? displayName : profileLoading ? '（读取中…）' : '未能读取'}
          </Typography.Text>
        </Typography.Paragraph>

        {profileError !== null && (
          <Alert
            type="error"
            showIcon
            style={{ marginBottom: 16 }}
            message="读不到当前账号，暂时不能批准"
            description={profileError}
            action={
              <Button size="small" onClick={() => void reload()}>
                重新读取
              </Button>
            }
          />
        )}

        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="只有当你刚刚在这台终端上发起登录时才继续"
          description="批准意味着把上面这个账号的登录状态交给那个终端。如果这个代码不是你自己发起的，请选择「拒绝」。"
        />

        {error !== null && <Alert type="error" message={error} style={{ marginBottom: 16 }} />}

        <Form<DeviceFormValues> form={form} layout="vertical" onFinish={() => void decide(true)}>
          <Form.Item
            name="userCode"
            label="终端上显示的代码"
            rules={[{ required: true, message: '请输入终端上显示的代码' }]}
          >
            <Input
              size="large"
              placeholder="XXXX-XXXX"
              autoComplete="off"
              autoFocus
              style={{ textAlign: 'center', letterSpacing: 2, textTransform: 'uppercase' }}
            />
          </Form.Item>
        </Form>

        <div style={{ display: 'flex', gap: 8 }}>
          <Button
            type="primary"
            block
            icon={<Check size={16} />}
            loading={submitting}
            disabled={!identityKnown}
            onClick={() => void form.submit()}
          >
            批准
          </Button>
          <Button
            danger
            block
            icon={<X size={16} />}
            disabled={submitting}
            onClick={() => void decide(false)}
          >
            拒绝
          </Button>
        </div>
      </Card>
    </div>
  )
}
