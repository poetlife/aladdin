import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Card, Space, Table, Tag, Typography } from 'antd'
import type { TableProps } from 'antd'
import { KeyRound, RefreshCw } from 'lucide-react'

import * as rbacApi from '../api/rbac'
import { messageOf, traceIdOf } from '../api/errors'
import { useSession } from '../auth'
import { PermissionCatalog } from '../gen/permission-catalog'
import type { PermissionCatalogEntry } from '../gen/permission-catalog'
import type { Role } from '../gen/proto/aladdin/rbac/v1/rbac_pb'

// failure 是一次失败的展示信息：给用户的文案，以及可拿去找日志的追踪 ID。
interface failure {
  message: string
  traceId: string | null
}

/**
 * 权限码目录页：只读对照——角色页里那一串码分别是什么意思。
 *
 * 码与说明来自生成的目录（`web/src/gen/permission-catalog.ts`，唯一信源是
 * `api/permissions/catalog.yaml`），因此这一页不需要任何请求就能回答"这个码是什么"。
 *
 * **「谁持有」只列直接声明的角色**：通配（`*`）与角色继承的展开是服务端的活，
 * 前端自己推一遍就是第二份实现，而两份迟早会不一致（见
 * docs/design/rbac/frontend-permissions.md 的「不得重复实现通配、继承」）。
 * 因此角色那一列只是返回数据上的字面成员测试，并在界面上说明它的边界。
 */
export function PermissionCatalogPage(): React.ReactNode {
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
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      setRoles([])
    } finally {
      setLoading(false)
    }
  }, [scope])

  useEffect(() => {
    void load()
  }, [load])

  const columns: NonNullable<TableProps<PermissionCatalogEntry>['columns']> = [
    {
      title: '权限码',
      dataIndex: 'code',
      key: 'code',
      render: (code: string) => <Typography.Text code>{code}</Typography.Text>,
    },
    { title: '说明', dataIndex: 'description', key: 'description' },
    {
      title: '直接声明它的角色',
      key: 'roles',
      render: (_, entry) => {
        if (failure !== null) {
          // 角色列表没读到，只说"列不出来"，不假装没有角色持有它——那会把一次
          // 读取失败说成一个错误结论。
          return <Typography.Text type="secondary">—</Typography.Text>
        }
        const holders = roles.filter((role) => role.permissions.includes(entry.code))
        if (holders.length === 0) {
          return <Typography.Text type="secondary">—</Typography.Text>
        }
        return (
          <Space wrap>
            {holders.map((role) => (
              <Tag key={role.id}>{role.displayName === '' ? role.id : role.displayName}</Tag>
            ))}
          </Space>
        )
      },
    },
  ]

  return (
    <Card
      title={
        <Space size={8}>
          <KeyRound size={16} />
          权限码目录
        </Space>
      }
      extra={
        <Button icon={<RefreshCw size={16} />} onClick={() => void load()}>
          刷新
        </Button>
      }
    >
      {failure !== null && (
        <Alert
          type="error"
          title={failure.message}
          description={
            <Typography.Text type="secondary">
              {failure.traceId !== null ? `追踪 ID：${failure.traceId}。` : ''}
              权限码与说明照常可读，只有「谁持有」这一列列不出来。
            </Typography.Text>
          }
          style={{ marginBottom: 16 }}
        />
      )}
      <Typography.Paragraph type="secondary">
        权限码是「领域.资源.动作」三段式，通配只能出现在末段或独占整个码。全集的唯一信源是
        <Typography.Text code>api/permissions/catalog.yaml</Typography.Text>
        ，两端常量都由它生成，因此这里列的就是系统的全部码。
      </Typography.Paragraph>
      <Table<PermissionCatalogEntry>
        rowKey="code"
        columns={columns}
        dataSource={PermissionCatalog}
        loading={loading}
        pagination={false}
        scroll={{ x: 'max-content' }}
      />
      {/* 通配与继承不在这里展开：`*` 持有者能做什么，由服务端在判定时展开。
          把这条边界说出来，读者才不会以为「直接声明」= 「只有这些角色能用」。 */}
      <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
        这一列只列
        <Typography.Text strong>直接声明</Typography.Text>
        它的角色；通配符（如 <Typography.Text code>*</Typography.Text>）与角色继承不在这里展开。
        某个主体实际生效的权限码，看概览页的「生效权限」。
      </Typography.Paragraph>
    </Card>
  )
}
