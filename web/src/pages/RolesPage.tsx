import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Card, Empty, Space, Table, Tag, Typography } from 'antd'
import type { TableProps } from 'antd'
import { RefreshCw, ShieldCheck } from 'lucide-react'

import * as rbacApi from '../api/rbac'
import { messageOf, traceIdOf } from '../api/errors'
import { useSession } from '../auth'
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
 * 角色定义页。
 *
 * 它列出的是**全库角色定义**：角色没有归属范围，因此这张表不随顶栏的管理范围
 * 过滤。顶栏在这里的唯一作用是判断"你有没有资格在这个范围上读角色"——切换范围
 * 让表格重取一次，内容不变，变的只是还能不能读。这句话必须写在界面上：只把
 * 顶栏和表格摆在一起而不解释，会被读成"改范围筛选了列表"。
 *
 * 界面上不出现没有真实语义的动作。这里原先有个「发布变更」按钮，而服务端对应的
 * `PublishPolicy` 目前是空实现（失效条目恒为 0），叫"发布"其实是重新读取——
 * 名实不符的按钮比没有按钮更糟，因此去掉，只留「刷新」。
 *
 * 它是"页面级裁剪"的示例：路由已由 RequirePermission 保证基础权限。
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
      title={
        <Space size={8}>
          <ShieldCheck size={16} />
          角色定义
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
            failure.traceId !== null && (
              <Typography.Text type="secondary" copyable>
                追踪 ID：{failure.traceId}
              </Typography.Text>
            )
          }
          style={{ marginBottom: 16 }}
        />
      )}
      {/* 先把"这张表是什么"说清楚，再摆表格。少了这一句，改管理范围之后
          列表纹丝不动会显得像没生效——而它本来就该纹丝不动。 */}
      <Typography.Paragraph type="secondary">
        这是全库的角色定义，不随顶栏的管理范围过滤。切换管理范围只影响「你能不能在这个范围上读它」，
        以及给谁在哪个范围上授予它——不改变这张表的内容。
      </Typography.Paragraph>
      <Table<Role>
        rowKey="id"
        columns={columns}
        dataSource={roles}
        loading={loading}
        pagination={false}
        // 三列都按内容撑宽，手机上装不下。让表格在自己的容器里横向滚动，
        // 而不是把整页顶宽——整页横向滚动是这一版要消掉的东西。
        // 代价是权限列不再换行、表格比桌面端更宽，这是"手机上读得下来"换来的。
        scroll={{ x: 'max-content' }}
        locale={{
          emptyText: (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="尚未定义任何角色" />
          ),
        }}
      />
    </Card>
  )
}
