import { Card, Descriptions, Empty, Space, Tag, Typography } from 'antd'

import { usePermissionSet, useSession } from '../auth'
import { useProfile } from '../profile'

/**
 * 首页：展示当前会话与生效权限。
 *
 * 它是排障时最有用的一页——"前端按钮消失"这类问题的第一步，
 * 就是确认这里显示的权限码集合是否符合预期。
 */
export function HomePage(): React.ReactNode {
  const { subject, scope } = useSession()
  const { profile } = useProfile()
  const permissions = usePermissionSet()
  const codes = permissions.toArray()

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Card title="当前会话">
        <Descriptions column={2} size="small">
          {/* 展示名由服务端算好：未设昵称时回退到登录渠道标识。把它与主体标识
              并排显示，是因为排障时常常需要把界面上看到的名字对应回库里那一行。 */}
          <Descriptions.Item label="显示名">{profile?.displayName ?? '—'}</Descriptions.Item>
          <Descriptions.Item label="主体标识">{subject?.subjectId ?? '—'}</Descriptions.Item>
          <Descriptions.Item label="类型">{subject?.subjectType ?? '—'}</Descriptions.Item>
          <Descriptions.Item label="当前作用域">{scope === '' ? '<global>' : scope}</Descriptions.Item>
          <Descriptions.Item label="凭证默认作用域">
            {subject?.defaultScope === '' ? '<global>' : (subject?.defaultScope ?? '—')}
          </Descriptions.Item>
        </Descriptions>
      </Card>

      <Card
        title="生效权限"
        extra={<Typography.Text type="secondary">由服务端展开，前端不做推导</Typography.Text>}
      >
        {codes.length === 0 ? (
          <Empty description="当前作用域下没有生效权限" />
        ) : (
          <Space wrap>
            {codes.map((code) => (
              <Tag key={code}>{code}</Tag>
            ))}
          </Space>
        )}
      </Card>
    </Space>
  )
}
