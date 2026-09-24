import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Card, Space, Table, Tag, Typography } from 'antd'
import type { TableProps } from 'antd'

import * as rbacApi from '../api/rbac'
import { messageOf } from '../api/errors'
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
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async (): Promise<void> => {
    setLoading(true)
    setError(null)
    try {
      const response = await rbacApi.listRoles(scope)
      setRoles(response.roles)
    } catch (err) {
      // messageOf 会区分鉴权拒绝与服务端其它错误，
      // 避免用户因为统一提示"加载失败"而去排查错误的方向。
      setError(messageOf(err))
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
      {error !== null && <Alert type="error" message={error} style={{ marginBottom: 16 }} />}
      <Table<Role> rowKey="id" columns={columns} dataSource={roles} loading={loading} pagination={false} />
    </Card>
  )
}
