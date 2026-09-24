import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Card, Space, Table, Tag, Typography } from 'antd'
import type { TableProps } from 'antd'

import * as rbacApi from '../api/rbac'
import { messageOf, traceIdOf } from '../api/errors'
import { PermissionGate, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import type { Role } from '../gen/proto/aladdin/rbac/v1/rbac_pb'

const columns: NonNullable<TableProps<Role>['columns']> = [
  {
    title: '标识',
    dataIndex: 'id',
    key: 'id',
    render: (id: string, role) => (
      <Space>
        <Typography.Text code>{id}</Typography.Text>
        {role.builtin && <Tag color="blue">内置</Tag>}
      </Space>
    ),
  },
  { title: '名称', dataIndex: 'displayName', key: 'displayName' },
  {
    title: '权限',
    dataIndex: 'permissions',
    key: 'permissions',
    render: (permissions: string[]) => (
      <Space wrap>
        {permissions.map((p) => (
          <Tag key={p}>{p}</Tag>
        ))}
      </Space>
    ),
  },
]

// failure 是一次失败的展示信息：给用户的文案，以及可拿去找日志的追踪 ID。
interface failure {
  message: string
  traceId: string | null
}

/**
 * 角色列表页。
 *
 * 它是"页面级裁剪"的示例：路由已由 RequirePermission 保证基础权限，
 * 页面内部再用 PermissionGate 处理粒度更细的写操作。
 */
export function RolesPage(): React.ReactNode {
  const { scope } = useSession()
  const [roles, setRoles] = useState<Role[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<failure | null>(null)

  const load = useCallback(async (): Promise<void> => {
    setLoading(true)
    setFailure(null)
    try {
      const response = await rbacApi.listRoles(scope)
      setRoles(response.roles)
    } catch (err) {
      // messageOf 会区分鉴权拒绝与服务端其它错误，
      // 避免用户因为统一提示"加载失败"而去排查错误的方向。
      // traceIdOf 取的是服务端回写的 trace-id：把它一并显示出来，
      // 用户报障时就不必描述"我什么时候点了什么"，直接给这一串即可对齐日志。
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      setRoles([])
    } finally {
      setLoading(false)
    }
  }, [scope])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <Card
      title="角色"
      extra={
        <Space>
          <PermissionGate require={PermissionCodes.RbacPolicyPublish}>
            <Button onClick={() => void load()}>发布变更</Button>
          </PermissionGate>
          <Button onClick={() => void load()}>刷新</Button>
        </Space>
      }
    >
      {failure !== null && (
        <Alert
          type="error"
          message={failure.message}
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
      <Table<Role> rowKey="id" columns={columns} dataSource={roles} loading={loading} pagination={false} />
    </Card>
  )
}
