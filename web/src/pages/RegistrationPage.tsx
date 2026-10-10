import { useCallback, useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Empty,
  Form,
  Input,
  InputNumber,
  Radio,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { TableProps } from 'antd'
import { RefreshCw, Ticket, UserPlus } from 'lucide-react'

import * as rbacApi from '../api/rbac'
import * as registrationApi from '../api/registration'
import { messageOf, traceIdOf } from '../api/errors'
import { PermissionGate, useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import { RegistrationMode } from '../gen/proto/aladdin/identity/v1/registration_pb'
import type { Invite } from '../gen/proto/aladdin/identity/v1/registration_pb'
import type { Role } from '../gen/proto/aladdin/rbac/v1/rbac_pb'
import { formatScope, useScopes } from '../rbac'
import { AppModal } from '../ui/AppModal'

/** 有效期候选。留空（值 0）表示不过期。 */
const EXPIRY_CHOICES = [
  { value: 0, label: '不过期' },
  { value: 1, label: '1 天' },
  { value: 7, label: '7 天' },
  { value: 30, label: '30 天' },
] as const

interface PolicyFormValues {
  mode: RegistrationMode
  // 两个**可空但必须存在**的字段：Select 的 allowClear 给出的就是 undefined，
  // 而这张表单的初值总是被一次 setFieldsValue 整体写入。
  defaultRoleId: string | undefined
  defaultScope: string | undefined
}

interface InviteFormValues {
  label?: string
  maxUses: number
  expiresInDays: number
}

/**
 * 注册页：谁能进来、进来拿什么。
 *
 * 它回答的是两件事，而它们在界面上**分成两块**：准入姿态（三选一）与新账号的默认
 * 角色，以及一对一张的邀请码（见 docs/design/rbac/management-ui.md）。
 *
 * 三条刻意的取舍：
 *
 *   - 准入姿态是**一组互斥选项**，不是三个各自独立的开关。"允许注册"与"需要邀请码"
 *     不可能同时为真或同时为假，拆成开关会造出两种没有意义的状态。
 *   - 默认角色与默认范围**成对出现**：要么都给、要么都不给，因此它们在同一张卡片里，
 *     而不是一个"可选角色"加一个总有效的范围框。
 *   - 默认角色的候选项**不在前端过滤**：能不能当默认角色由服务端的约束校验说了算
 *     （见 docs/design/identity/registration.md），前端自己维护一份判断就是第二份实现。
 *     服务端拒绝时把理由原样显示。
 *
 * 邀请码的**明文只在签发那一刻出现一次**，此后任何页面都读不回来——因此它用一个
 * 关不掉的提示留在页面上，直到管理员开始下一件事。
 */
export function RegistrationPage(): React.ReactNode {
  const { scope } = useSession()
  const { scopes } = useScopes()

  const [policy, setPolicy] = useState<registrationApi.RegistrationPolicy | null>(null)
  const [invites, setInvites] = useState<Invite[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [loading, setLoading] = useState(true)
  const [readFailure, setReadFailure] = useState<{ message: string; traceId: string | null } | null>(null)

  const [saving, setSaving] = useState(false)
  const [issuing, setIssuing] = useState(false)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  // 刚签发的那一份码的明文。它只在这里存在一次；下一次签发会把它换掉。
  const [issuedCode, setIssuedCode] = useState<string | null>(null)

  const [policyForm] = Form.useForm<PolicyFormValues>()
  const [inviteForm] = Form.useForm<InviteFormValues>()
  const [revoking, setRevoking] = useState<Invite | null>(null)

  const canWrite = useAnyPermission([PermissionCodes.IdentityRegistrationWrite])

  const reload = useCallback(async () => {
    setLoading(true)
    setReadFailure(null)
    try {
      const [next, list] = await Promise.all([
        registrationApi.getRegistrationPolicy(scope),
        registrationApi.listInvites(scope),
      ])
      setPolicy(next)
      setInvites(list)
      policyForm.setFieldsValue({
        mode: next.mode,
        defaultRoleId: next.defaultRoleId === '' ? undefined : next.defaultRoleId,
        defaultScope: next.defaultScope === '' ? undefined : next.defaultScope,
      })
    } catch (err) {
      setReadFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setLoading(false)
    }
  }, [scope, policyForm])

  useEffect(() => {
    void reload()
  }, [reload])

  // 默认角色的候选项来自角色目录。读不到角色时**不让整页失败**：邀请码那一半
  // 与它无关，而这一半只是少了一个下拉的候选。
  useEffect(() => {
    let cancelled = false
    void rbacApi
      .listRoles(scope)
      .then((resp) => {
        if (!cancelled) setRoles(resp.roles)
      })
      .catch(() => {
        if (!cancelled) setRoles([])
      })
    return () => {
      cancelled = true
    }
  }, [scope])

  async function handleSavePolicy(values: PolicyFormValues): Promise<void> {
    setSaving(true)
    setActionFailure(null)
    try {
      const next = await registrationApi.putRegistrationPolicy(
        scope,
        values.mode,
        values.defaultRoleId?.trim() ?? '',
        values.defaultScope?.trim() ?? '',
      )
      setPolicy(next)
    } catch (err) {
      setActionFailure(messageOf(err))
    } finally {
      setSaving(false)
    }
  }

  async function handleIssue(values: InviteFormValues): Promise<void> {
    setIssuing(true)
    setActionFailure(null)
    setIssuedCode(null)
    try {
      const days = values.expiresInDays ?? 0
      const expiresAt =
        days > 0 ? new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString() : ''
      const { code } = await registrationApi.createInvite(
        scope,
        values.label?.trim() ?? '',
        values.maxUses ?? 0,
        expiresAt,
      )
      setIssuedCode(code)
      inviteForm.resetFields()
      await reload()
    } catch (err) {
      setActionFailure(messageOf(err))
    } finally {
      setIssuing(false)
    }
  }

  async function handleRevoke(): Promise<void> {
    if (revoking === null) return
    setActionFailure(null)
    try {
      await registrationApi.revokeInvite(scope, revoking.id)
      setRevoking(null)
      await reload()
    } catch (err) {
      setActionFailure(messageOf(err))
    }
  }

  const inviteColumns: NonNullable<TableProps<Invite>['columns']> = [
    {
      title: '说明',
      dataIndex: 'label',
      key: 'label',
      render: (label: string) =>
        label === '' ? <Typography.Text type="secondary">—</Typography.Text> : label,
    },
    {
      title: '用量',
      key: 'uses',
      render: (_: unknown, row: Invite) =>
        row.maxUses === 0 ? `${row.usedCount} / 不限` : `${row.usedCount} / ${row.maxUses}`,
    },
    {
      title: '有效期',
      dataIndex: 'expiresAt',
      key: 'expiresAt',
      render: (at: string) =>
        at === '' ? <Typography.Text type="secondary">不过期</Typography.Text> : describeTime(at),
    },
    {
      title: '状态',
      key: 'state',
      render: (_: unknown, row: Invite) => <InviteState invite={row} />,
    },
    {
      title: '签发',
      key: 'created',
      render: (_: unknown, row: Invite) => (
        <Space orientation="vertical" size={0}>
          <Typography.Text>{describeTime(row.createdAt)}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {row.createdBySubjectId}
          </Typography.Text>
        </Space>
      ),
    },
    ...(canWrite
      ? [
          {
            title: '操作',
            key: 'actions',
            render: (_: unknown, row: Invite) =>
              row.revokedAt === '' ? (
                <Button type="link" danger onClick={() => setRevoking(row)}>
                  撤销
                </Button>
              ) : (
                <Typography.Text type="secondary">已撤销</Typography.Text>
              ),
          },
        ]
      : []),
  ]

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <UserPlus size={16} />
            准入与默认角色
          </Space>
        }
        extra={
          <Button icon={<RefreshCw size={16} />} onClick={() => void reload()} loading={loading}>
            刷新
          </Button>
        }
      >
        {readFailure !== null && (
          <Alert
            type="error"
            title={readFailure.message}
            description={
              readFailure.traceId !== null && (
                <Typography.Text type="secondary" copyable>
                  追踪 ID：{readFailure.traceId}
                </Typography.Text>
              )
            }
            style={{ marginBottom: 16 }}
          />
        )}

        <Typography.Paragraph type="secondary">
          这里决定的是
          <Typography.Text strong>未登记身份</Typography.Text>
          的待遇：已经在册的账号不受这三项影响，照常登录。默认角色是在注册那一刻写成一条真实的角色绑定，因此改它
          <Typography.Text strong>不追溯</Typography.Text>
          已经注册的账号。
        </Typography.Paragraph>

        {actionFailure !== null && (
          <Alert type="error" title={actionFailure} style={{ marginBottom: 16 }} />
        )}

        <PermissionGate
          require={PermissionCodes.IdentityRegistrationWrite}
          fallback={
            <Typography.Text type="secondary">
              你没有修改注册策略的权限，这一页只读。
            </Typography.Text>
          }
        >
          <Form<PolicyFormValues>
            form={policyForm}
            layout="vertical"
            onFinish={(values) => void handleSavePolicy(values)}
          >
            <Form.Item name="mode" label="未登记的渠道身份来登录时">
              <Radio.Group>
                <Space orientation="vertical">
                  <Radio value={RegistrationMode.OPEN}>直接登记成新账号（开放注册）</Radio>
                  <Radio value={RegistrationMode.INVITE}>先要一份有效的邀请码</Radio>
                  <Radio value={RegistrationMode.CLOSED}>拒绝，不接受新账号</Radio>
                </Space>
              </Radio.Group>
            </Form.Item>
            <Space size="middle" align="start" wrap>
              <Form.Item
                name="defaultRoleId"
                label="新账号的默认角色"
                extra="留空表示不给任何角色。留空时下面的范围也留空。"
                style={{ minWidth: 240 }}
              >
                <Select
                  allowClear
                  placeholder="不给默认角色"
                  options={roles.map((role) => ({ value: role.id, label: role.displayName }))}
                  notFoundContent="没有读到角色目录"
                />
              </Form.Item>
              <Form.Item
                name="defaultScope"
                label="默认角色的范围"
                extra="留空表示全局。它同时成为新账号的默认作用域。"
                style={{ minWidth: 240 }}
              >
                <Select
                  allowClear
                  placeholder="全局"
                  options={[
                    { value: '', label: '全局' },
                    ...scopes.map((item) => ({
                      value: item.path,
                      label: formatScope(item.path),
                    })),
                  ]}
                />
              </Form.Item>
            </Space>
            <div>
              <Button type="primary" htmlType="submit" loading={saving}>
                保存
              </Button>
              {policy !== null && policy.updatedBySubjectId !== '' && (
                <Typography.Text type="secondary" style={{ marginLeft: 12 }}>
                  上次改动：{policy.updatedBySubjectId}，{describeTime(policy.updatedAt)}
                </Typography.Text>
              )}
            </div>
          </Form>
        </PermissionGate>
      </Card>

      <Card
        title={
          <Space size={8}>
            <Ticket size={16} />
            邀请码
          </Space>
        }
      >
        <Typography.Paragraph type="secondary">
          邀请码只决定
          <Typography.Text strong>能不能进来</Typography.Text>
          ，不带角色、不带范围、不绑定收件人。进来之后拿什么，由上面那三项决定。
        </Typography.Paragraph>

        {issuedCode !== null && (
          <Alert
            type="success"
            style={{ marginBottom: 16 }}
            title="邀请码已签发——这是它唯一一次出现"
            description={
              <Space orientation="vertical" size={4}>
                <Typography.Text code copyable style={{ fontSize: 16 }}>
                  {issuedCode}
                </Typography.Text>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  服务端只保存它的摘要，这个列表再也读不回明文。请现在把它交给使用的人。
                </Typography.Text>
              </Space>
            }
          />
        )}

        <Table<Invite>
          rowKey="id"
          columns={inviteColumns}
          dataSource={invites}
          loading={loading}
          pagination={false}
          scroll={{ x: 'max-content' }}
          locale={{
            emptyText: (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description="还没有签发过邀请码"
              />
            ),
          }}
        />
      </Card>

      <Card title="签发一份邀请码">
        <PermissionGate
          require={PermissionCodes.IdentityRegistrationWrite}
          fallback={
            <Typography.Text type="secondary">
              你没有签发邀请码的权限，这一页只读。
            </Typography.Text>
          }
        >
          <Form<InviteFormValues>
            form={inviteForm}
            layout="vertical"
            initialValues={{ maxUses: 1, expiresInDays: 7 }}
            onFinish={(values) => void handleIssue(values)}
          >
            <Space size="middle" align="start" wrap>
              <Form.Item
                name="label"
                label="说明"
                extra="只给自己看，不参与任何判断"
                style={{ minWidth: 240 }}
              >
                <Input placeholder="比如：给张三" autoComplete="off" />
              </Form.Item>
              <Form.Item
                name="maxUses"
                label="可用次数"
                extra="0 表示不限次"
                style={{ minWidth: 160 }}
              >
                <InputNumber min={0} precision={0} style={{ width: '100%' }} />
              </Form.Item>
              <Form.Item name="expiresInDays" label="有效期" style={{ minWidth: 160 }}>
                <Select options={EXPIRY_CHOICES.map((c) => ({ value: c.value, label: c.label }))} />
              </Form.Item>
            </Space>
            <div>
              <Button type="primary" htmlType="submit" loading={issuing}>
                签发
              </Button>
            </div>
          </Form>
        </PermissionGate>
      </Card>

      <AppModal
        title="撤销这份邀请码？"
        open={revoking !== null}
        okText="撤销"
        okButtonProps={{ danger: true }}
        cancelText="取消"
        onOk={() => void handleRevoke()}
        onCancel={() => setRevoking(null)}
      >
        <Typography.Paragraph style={{ marginBottom: 8 }}>
          撤销之后这份码立即不可兑换。<Typography.Text strong>已经用它注册进来的账号
          不受影响</Typography.Text>——它们拿到的角色绑定是真实的记录，与这份码再无关系。
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          撤销不删除记录：用量与签发人留着，方便日后回答"这个码被谁用过、用过几次"。
        </Typography.Paragraph>
      </AppModal>
    </Space>
  )
}

/** 一份邀请码当前的状态。它是**展示**，不是判定——判定在服务端的原子扣减里。 */
function InviteState({ invite }: { invite: Invite }): React.ReactNode {
  if (invite.revokedAt !== '') {
    return <Tag>已撤销</Tag>
  }
  if (invite.expiresAt !== '' && Date.parse(invite.expiresAt) <= Date.now()) {
    return <Tag>已过期</Tag>
  }
  if (invite.maxUses > 0 && invite.usedCount >= invite.maxUses) {
    return <Tag>已用尽</Tag>
  }
  return <Tag color="green">可用</Tag>
}

/** 把 ISO 时间折成本地可读的写法。 */
function describeTime(iso: string): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) {
    return iso
  }
  return at.toLocaleString()
}
