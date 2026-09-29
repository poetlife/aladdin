import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Collapse,
  Empty,
  Form,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { TableProps } from 'antd'
import { RefreshCw, UserCog, UserPlus } from 'lucide-react'

import * as rbacApi from '../api/rbac'
import { messageOf, traceIdOf } from '../api/errors'
import { PermissionGate, useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import { formatScope, GLOBAL_SCOPE_LABEL, useScopes } from '../rbac'
import type { Role, RoleBinding } from '../gen/proto/aladdin/rbac/v1/rbac_pb'

// failure 是一次失败的展示信息：给用户的文案，以及可拿去找日志的追踪 ID。
interface failure {
  message: string
  traceId: string | null
}

interface GrantFormValues {
  roleId: string
  scope: string
}

/**
 * 人员授权页：某个主体持有哪些角色，以及授予与回收。
 *
 * 它以**主体标识**为键，而不是邮箱或登录名：服务端没有"主体标识 → 可读名字"
 * 的解析入口（档案只服务主体自己），也没有"按范围列出全部绑定"的查询。
 * 因此这一页先查一个主体，再在他身上做增删；界面必须把这条局限说出来，
 * 否则输错邮箱的人会以为是自己查错了（见 docs/design/rbac/management-ui.md）。
 *
 * 授予只在**主体确实已被登记**时可用：服务端出于"空主体认领"的需要，
 * 允许给尚未登记的主体写绑定，而界面上能发生这件事的唯一原因是标识打错了
 * ——那会留下一条谁也认领不了的悬空绑定。因此这里在按钮上就禁掉并说明原因，
 * 而不是等人点了才报错。
 */
export function SubjectBindingsPage(): React.ReactNode {
  const { subject, scope } = useSession()

  const [subjectDraft, setSubjectDraft] = useState('')
  // 已查询的主体。与输入框分开：输入框可以改到一半，而展示的表格必须对应
  // "上一次真正查过的那个主体"，否则表格会跟着每次击键消失又出现。
  const [queried, setQueried] = useState('')
  const [bindings, setBindings] = useState<RoleBinding[]>([])
  const [effective, setEffective] = useState<string[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [loading, setLoading] = useState(false)
  const [failure, setFailure] = useState<failure | null>(null)
  // 查询失败过（多半是主体不存在）。它决定授予能不能用。
  const [unknownSubject, setUnknownSubject] = useState(false)

  const [granting, setGranting] = useState(false)
  const [grantFailure, setGrantFailure] = useState<string | null>(null)
  const [granted, setGranted] = useState(false)
  const [revoking, setRevoking] = useState(false)
  const [form] = Form.useForm<GrantFormValues>()

  // 可授予的范围来自范围目录（见 docs/design/rbac/scopes.md）。
  const registered = useScopes()

  const query = useCallback(
    async (raw: string): Promise<void> => {
      const target = raw.trim()
      if (target === '') {
        setQueried('')
        setBindings([])
        setEffective([])
        setUnknownSubject(false)
        setFailure(null)
        return
      }
      setLoading(true)
      setFailure(null)
      try {
        const response = await rbacApi.listSubjectBindings(scope, target)
        setQueried(target)
        setBindings(response.bindings)
        setEffective(response.effectivePermissions)
        setUnknownSubject(false)
      } catch (err) {
        // messageOf 会区分鉴权拒绝与服务端其它错误；traceIdOf 取服务端回写的
        // trace-id，用户报障时直接给这一串即可对齐日志。
        setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
        setQueried(target)
        setBindings([])
        setEffective([])
        setUnknownSubject(true)
      } finally {
        setLoading(false)
      }
    },
    [scope],
  )

  // 角色选项：授予时要从中选一个。它不随主体变，但随管理范围重取——
  // 这个查询本身要在当前范围上获得鉴权。
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await rbacApi.listRoles(scope)
        if (!cancelled) setRoles(response.roles)
      } catch {
        // 角色列表拉不到只影响"授予"这一块的可选项，不影响查看绑定；
        // 这一页的主体不是它，不为此摆一条告警。
        if (!cancelled) setRoles([])
      }
    })()
    return () => {
      cancelled = true
    }
  }, [scope])

  // 授予范围跟着**当前管理范围**走：那个范围是"我现在在哪儿工作"，
  // 授予的默认落点就该在那儿，而不是上次留下的旧值。这里存的是**范围值**
  //（空串 = 全局），不是界面写法——下拉框的取值就是它。
  useEffect(() => {
    form.setFieldsValue({ scope })
  }, [scope, form])

  // 进来先查自己：多数时候管理员要做的第一件事就是核对自己现在有什么。
  // 只在首次拿到主体时做一次，之后不再覆盖用户的选择。
  const initialized = useRef(false)
  useEffect(() => {
    const me = subject?.subjectId ?? ''
    if (initialized.current || me === '') return
    initialized.current = true
    setSubjectDraft(me)
    void query(me)
  }, [subject, query])

  async function handleGrant(values: GrantFormValues): Promise<void> {
    setGranting(true)
    setGrantFailure(null)
    setGranted(false)
    try {
      // 请求里的范围既是**鉴权的范围**，也是**写进绑定的范围**——服务端只有
      // 这一个字段。因此要授到某个范围，就得在那个范围上有授予权限。
      await rbacApi.assignRole(values.scope.trim(), queried, values.roleId, true)
      setGranted(true)
      await query(queried)
    } catch (err) {
      setGrantFailure(messageOf(err))
    } finally {
      setGranting(false)
    }
  }

  async function handleRevoke(binding: RoleBinding): Promise<void> {
    setRevoking(true)
    setGrantFailure(null)
    setGranted(false)
    try {
      // 请求里的范围必须是**这条绑定自己的范围**，不是当前管理范围：服务端按
      // （主体, 角色, 范围）三元组定位那条绑定，带错范围删到的是另一条不存在的，
      // 而 Unbind 对"本来就没有"是幂等的——于是它会静默地什么也不做。
      // 鉴权也在那个范围上做：要收回某范围的授权，你得在那个范围上有权。
      await rbacApi.assignRole(binding.scope, binding.subjectId, binding.roleId, false)
      await query(queried)
    } catch (err) {
      setGrantFailure(messageOf(err))
    } finally {
      setRevoking(false)
    }
  }

  const roleNames = new Map(roles.map((r) => [r.id, r.displayName]))

  // 授予的目标范围只能从**已登记**的范围里选：服务端会拒绝未登记的范围，与其让人
  // 输一个必然被拒的字符串，不如在源头就不给选。「全局」在这里是**合法**目标——
  // 管理面的空范围就是全局（与会话查询里"不指定"的含义不同，见
  // docs/design/rbac/scopes.md）。
  const scopeOptions = [
    { value: '', label: GLOBAL_SCOPE_LABEL },
    ...registered.scopes.map((s) => ({
      value: s.path,
      label: s.displayName === '' ? s.path : `${s.displayName}（${s.path}）`,
    })),
  ]

  // 「操作」整列随权限出现或消失。PermissionGate 包不了**列定义**（它不是控件），
  // 因此这里用同一个权限入口做集合成员测试——判定语义仍然只有那一处。
  // 不渲染而不是留一列灰按钮：没有授予权限的人根本不需要看到这一列。
  const canAssign = useAnyPermission([PermissionCodes.RbacSubjectAssign])

  const columns: NonNullable<TableProps<RoleBinding>['columns']> = [
    {
      title: '角色',
      dataIndex: 'roleId',
      key: 'roleId',
      render: (roleId: string) => (
        <Space size={8}>
          <Typography.Text code>{roleId}</Typography.Text>
          {roleNames.get(roleId) !== undefined && (
            <Typography.Text type="secondary">{roleNames.get(roleId)}</Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: '作用域',
      dataIndex: 'scope',
      key: 'scope',
      render: (value: string) => formatScope(value),
    },
    ...(canAssign
      ? [
          {
            title: '操作',
            key: 'actions',
            // 行内操作是一串文字按钮（link），与身份卡片的「解绑」同一套写法。
            render: (_: unknown, binding: RoleBinding) => (
              <Button danger type="link" disabled={revoking} onClick={() => void handleRevoke(binding)}>
                回收
              </Button>
            ),
          },
        ]
      : []),
  ]

  const roleOptions = roles.map((r) => ({
    value: r.id,
    label: r.displayName === '' ? r.id : `${r.displayName}（${r.id}）`,
  }))

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <UserCog size={16} />
            人员授权
          </Space>
        }
      >
        <Space.Compact style={{ width: '100%', maxWidth: 480 }}>
          <Input
            value={subjectDraft}
            onChange={(e) => setSubjectDraft(e.target.value)}
            onPressEnter={() => void query(subjectDraft)}
            placeholder="主体标识"
            aria-label="主体标识"
            allowClear
          />
          <Button type="primary" loading={loading} onClick={() => void query(subjectDraft)}>
            查询
          </Button>
        </Space.Compact>
        {/* 「谁是谁」在这套系统里只能靠主体标识。这句话是必要的：没有它，
            输邮箱查不到东西的人会反复怀疑是不是自己查错了。 */}
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          只能按主体标识查询：服务端不提供"邮箱 / 登录名 → 主体"的反查，也不解析展示名。
          主体标识在主体首次登录时由系统分配，复制它来查即可。
        </Typography.Paragraph>
      </Card>

      <Card
        title="角色绑定"
        extra={
          <Button
            icon={<RefreshCw size={16} />}
            disabled={queried === ''}
            onClick={() => void query(queried)}
          >
            刷新
          </Button>
        }
      >
        {failure !== null && (
          <Alert
            type="error"
            title={failure.message}
            description={
              failure.traceId !== null && (
                <Typography.Text type="secondary" copyable>
                  追踪 ID：{failure.traceId}
                </Typography.Text>
              )
            }
            style={{ marginBottom: 16 }}
          />
        )}
        {grantFailure !== null && (
          <Alert type="error" title={grantFailure} style={{ marginBottom: 16 }} />
        )}
        {granted && <Alert type="success" title="已更新" style={{ marginBottom: 16 }} />}

        {queried === '' ? (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="先在上方输入主体标识并查询"
          />
        ) : failure !== null ? null : (
          <>
            <Table<RoleBinding>
              rowKey={(b) => `${b.subjectId}|${b.roleId}|${b.scope}`}
              columns={columns}
              dataSource={bindings}
              loading={loading}
              pagination={false}
              scroll={{ x: 'max-content' }}
              locale={{
                emptyText: (
                  <Empty
                    image={Empty.PRESENTED_IMAGE_SIMPLE}
                    description="该主体没有任何角色绑定"
                  />
                ),
              }}
            />
            {/* 展开后的权限码是排障用的：判断"他为什么能做这件事"时要看这里，
                而日常授权看的是上面那张绑定表。 */}
            <Collapse
              ghost
              style={{ marginTop: 8 }}
              items={[
                {
                  key: 'effective',
                  label: `展开后的权限（${effective.length}）`,
                  children:
                    effective.length === 0 ? (
                      <Typography.Text type="secondary">
                        该主体在此绑定下没有任何生效权限。
                      </Typography.Text>
                    ) : (
                      <Space wrap>
                        {effective.map((code) => (
                          <Tag key={code}>{code}</Tag>
                        ))}
                      </Space>
                    ),
                },
              ]}
            />
          </>
        )}
      </Card>

      <Card
        title={
          <Space size={8}>
            <UserPlus size={16} />
            授予角色
          </Space>
        }
      >
        <Form<GrantFormValues>
          form={form}
          layout="vertical"
          initialValues={{ scope }}
          onFinish={(values) => void handleGrant(values)}
        >
          <Form.Item name="roleId" label="角色" rules={[{ required: true, message: '请选择一个角色' }]}>
            <Select
              options={roleOptions}
              placeholder="选择要授予的角色"
              showSearch
              optionFilterProp="label"
            />
          </Form.Item>
          <Form.Item
            name="scope"
            label="授予在哪个范围"
            extra="这个范围既是判定的范围，也会写进绑定。范围必须是「范围」页里登记过的——服务端会拒绝未登记的范围，所以这里只给选，不给猜。"
          >
            <Select
              options={scopeOptions}
              showSearch
              optionFilterProp="label"
              placeholder="选择范围"
            />
          </Form.Item>
          <PermissionGate
            require={PermissionCodes.RbacSubjectAssign}
            fallback={
              <Typography.Text type="secondary">
                你没有授予角色的权限，这一块只读。
              </Typography.Text>
            }
          >
            <Button
              type="primary"
              htmlType="submit"
              loading={granting}
              disabled={!grantable(queried, unknownSubject)}
            >
              授予
            </Button>
          </PermissionGate>
        </Form>
        {queried === '' && (
          <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
            先查询一个主体，再给他授予角色。
          </Typography.Paragraph>
        )}
        {queried !== '' && unknownSubject && (
          <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
            「{queried}」不存在或尚未登记，无法授予——请先核对主体标识。
          </Typography.Paragraph>
        )}
        {/* 目录读不到或还是空的时，这里只剩「全局」可选。说出来，否则人手输一个
            范围会收到"范围未登记"而不知道为什么。 */}
        {registered.scopes.length === 0 && (
          <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
            暂时列不出已登记的范围（没有读范围目录的权限，或这个部署还没登记过任何范围）。
            除「全局」外要授到具体范围，请先到「范围」页登记它。
          </Typography.Paragraph>
        )}
      </Card>
    </Space>
  )
}

/**
 * 能不能授予。
 *
 * 前提是"已经查到一个**真实存在**的主体"：给不存在的标识写绑定，服务端不会
 * 拒绝（它容许尚未登记的主体，那是空主体认领流程需要的），于是会留下一条
 * 谁也认领不了的悬空绑定。所以这里把前提摆到按钮上。
 */
function grantable(queried: string, unknownSubject: boolean): boolean {
  return queried !== '' && !unknownSubject
}
