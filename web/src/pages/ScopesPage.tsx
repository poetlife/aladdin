import { useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Empty,
  Form,
  Input,
  Popconfirm,
  Space,
  Table,
  Typography,
} from 'antd'
import type { TableProps } from 'antd'
import { FolderTree, RefreshCw, Trash2 } from 'lucide-react'

import * as rbacApi from '../api/rbac'
import { messageOf, traceIdOf } from '../api/errors'
import { PermissionGate, useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import { useScopes } from '../rbac'
import { AppModal } from '../ui/AppModal'
import type { Scope } from '../gen/proto/aladdin/rbac/v1/rbac_pb'

interface CreateFormValues {
  path: string
  displayName?: string
}

/**
 * 范围页：登记这个部署里有哪些范围。
 *
 * 它回答的是"系统里有哪些范围"，而不是"我在哪个范围里工作"——后者是顶栏那个
 * 控件（见 docs/design/rbac/management-ui.md）。两者在一处交错：**绑定只能指向
 * 已登记的范围**，因此给人授权之前要先在这里把范围登记出来。
 *
 * 路径是标识，**不可更改**：这一页只能改显示名。要"改路径"就是新建一个、把
 * 绑定迁过去、再删掉旧的——与角色标识同构（见 docs/design/rbac/scopes.md）。
 *
 * 删除不可撤回，因此二次确认；范围内的引用还在时服务端会拒绝，界面把那条理由
 * 原样呈现（"仍被 N 条绑定引用"），而不是自己判断能不能删。
 */
export function ScopesPage(): React.ReactNode {
  // 列表来自外壳持有的那一份，不自己再拉一份：**同一条事实只能有一份状态**。
  // 顶栏的候选读的是同一份，因此这里增删之后候选立刻跟着变。
  const { scopes, loading, error, reload } = useScopes()
  const { scope } = useSession()

  const [creating, setCreating] = useState(false)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [done, setDone] = useState<string | null>(null)

  const [renaming, setRenaming] = useState<Scope | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [renameBusy, setRenameBusy] = useState(false)

  const [form] = Form.useForm<CreateFormValues>()

  const canWrite = useAnyPermission([PermissionCodes.RbacScopeWrite])

  // 读取失败由这一页负责说人话（provider 只把原因原样递过来）：它是这一页的
  // 主体数据，不像顶栏那样可以默默退回别的来源。
  const failure =
    error === null ? null : { message: messageOf(error), traceId: traceIdOf(error) }

  async function handleCreate(values: CreateFormValues): Promise<void> {
    setCreating(true)
    setActionFailure(null)
    setDone(null)
    try {
      await rbacApi.putScope(scope, values.path.trim(), values.displayName?.trim() ?? '')
      setDone(`已登记范围 ${values.path.trim()}`)
      form.resetFields()
      await reload()
    } catch (err) {
      setActionFailure(messageOf(err))
    } finally {
      setCreating(false)
    }
  }

  async function handleRename(): Promise<void> {
    if (renaming === null) return
    setRenameBusy(true)
    setActionFailure(null)
    setDone(null)
    try {
      await rbacApi.putScope(scope, renaming.path, renameValue.trim())
      setDone(`已更新 ${renaming.path} 的显示名`)
      setRenaming(null)
      await reload()
    } catch (err) {
      setActionFailure(messageOf(err))
    } finally {
      setRenameBusy(false)
    }
  }

  async function handleDelete(path: string): Promise<void> {
    setActionFailure(null)
    setDone(null)
    try {
      await rbacApi.deleteScope(scope, path)
      setDone(`已删除范围 ${path}`)
      await reload()
    } catch (err) {
      // 多半是"仍被 N 条绑定引用"——那是服务端的判断，原样呈现。
      setActionFailure(messageOf(err))
    }
  }

  const columns: NonNullable<TableProps<Scope>['columns']> = [
    {
      title: '路径',
      dataIndex: 'path',
      key: 'path',
      render: (path: string) => <Typography.Text code>{path}</Typography.Text>,
    },
    {
      title: '显示名',
      dataIndex: 'displayName',
      key: 'displayName',
      render: (name: string) =>
        name === '' ? <Typography.Text type="secondary">—</Typography.Text> : name,
    },
    // 「操作」整列随权限出现或消失：PermissionGate 包不了列定义（它不是控件），
    // 因此这里用同一个权限入口做集合成员测试。
    ...(canWrite
      ? [
          {
            title: '操作',
            key: 'actions',
            render: (_: unknown, row: Scope) => (
              // 行内操作是一串文字按钮（link），与角色页、工程列表页同一套写法：
              // 表格里一排带边框的按钮会把这一列变成视觉主体，而它只是次要操作。
              <Space>
                <Button
                  type="link"
                  onClick={() => {
                    setRenaming(row)
                    setRenameValue(row.displayName)
                  }}
                >
                  改显示名
                </Button>
                <Popconfirm
                  title={`删除范围 ${row.path}？`}
                  description="不可逆。该范围及其后代上的绑定必须先清空，否则会被拒绝。"
                  okText="删除"
                  okButtonProps={{ danger: true }}
                  cancelText="取消"
                  onConfirm={() => void handleDelete(row.path)}
                >
                  <Button type="link" danger icon={<Trash2 size={14} />}>
                    删除
                  </Button>
                </Popconfirm>
              </Space>
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
            <FolderTree size={16} />
            范围
          </Space>
        }
        extra={
          <Button icon={<RefreshCw size={16} />} onClick={() => void reload()}>
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
        {/* 先把这一页与顶栏的关系说清：一个是目录，一个是"我在哪儿"。 */}
        <Typography.Paragraph type="secondary">
          角色绑定只能指向
          <Typography.Text strong>已登记</Typography.Text>
          的范围，因此给人授权之前要先在这里登记。路径是标识，一经登记不可更改——这一页只能
          改显示名；要换路径就是新建一个、把绑定迁过去、再删掉旧的。
        </Typography.Paragraph>

        {actionFailure !== null && (
          <Alert type="error" title={actionFailure} style={{ marginBottom: 16 }} />
        )}
        {done !== null && <Alert type="success" title={done} style={{ marginBottom: 16 }} />}

        <Table<Scope>
          rowKey="path"
          columns={columns}
          dataSource={scopes}
          loading={loading}
          pagination={false}
          scroll={{ x: 'max-content' }}
          locale={{
            emptyText: (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description="还没有登记任何范围；下面登记一个，或先跑一遍引导配置"
              />
            ),
          }}
        />
      </Card>

      <Card title="登记一个范围">
        <PermissionGate
          require={PermissionCodes.RbacScopeWrite}
          fallback={
            <Typography.Text type="secondary">
              你没有登记范围的权限，这一页只读。
            </Typography.Text>
          }
        >
          <Form<CreateFormValues>
            form={form}
            layout="vertical"
            onFinish={(values) => void handleCreate(values)}
          >
            <Form.Item
              name="path"
              label="路径"
              extra="形如 tenant/acme。它是标识，登记之后不能改；不要求上级范围已经登记。"
              rules={[{ required: true, message: '请填一个路径' }]}
            >
              <Input placeholder="tenant/acme" autoComplete="off" />
            </Form.Item>
            <Form.Item name="displayName" label="显示名" extra="留空则界面显示路径本身">
              <Input placeholder="比如：Acme 事业部" autoComplete="off" />
            </Form.Item>
            <Button type="primary" htmlType="submit" loading={creating}>
              登记
            </Button>
          </Form>
        </PermissionGate>
      </Card>

      <AppModal
        title={renaming === null ? '改显示名' : `改 ${renaming.path} 的显示名`}
        open={renaming !== null}
        confirmLoading={renameBusy}
        okText="保存"
        cancelText="取消"
        onOk={() => void handleRename()}
        onCancel={() => setRenaming(null)}
      >
        <Input
          value={renameValue}
          onChange={(e) => setRenameValue(e.target.value)}
          placeholder="留空则界面显示路径本身"
          aria-label="显示名"
        />
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          路径不可更改。显示名只用于展示，不参与任何判定。
        </Typography.Paragraph>
      </AppModal>
    </Space>
  )
}
