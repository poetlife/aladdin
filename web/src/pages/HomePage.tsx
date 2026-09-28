import { Card, Descriptions, Empty, Skeleton, Space, Tag, Typography } from 'antd'
import { LayoutDashboard, ShieldCheck } from 'lucide-react'

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
  const { profile, loading: profileLoading } = useProfile()
  const permissions = usePermissionSet()
  const codes = permissions.toArray()

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <LayoutDashboard size={16} />
            当前会话
          </Space>
        }
      >
        <Descriptions column={{ xs: 1, sm: 1, md: 2 }} size="small">
          {/* 展示名由服务端算好：未设昵称时回退到登录渠道标识。把它与主体标识
              并排显示，是因为排障时常常需要把界面上看到的名字对应回库里那一行。 */}
          {/* 档案是这一页唯一晚到的数据（会话与权限码在进来之前就已就绪），
              所以只有这一格需要占位；整页铺骨架反而会把已经拿到的数据盖掉。 */}
          <Descriptions.Item label="显示名">
            {profileLoading && profile === null ? (
              <Skeleton.Input active size="small" style={{ width: 120 }} />
            ) : (
              (profile?.displayName ?? '—')
            )}
          </Descriptions.Item>
          <Descriptions.Item label="主体标识">{subject?.subjectId ?? '—'}</Descriptions.Item>
          <Descriptions.Item label="类型">{subject?.subjectType ?? '—'}</Descriptions.Item>
          <Descriptions.Item label="当前作用域">{scope === '' ? '<global>' : scope}</Descriptions.Item>
          <Descriptions.Item label="凭证默认作用域">
            {subject?.defaultScope === '' ? '<global>' : (subject?.defaultScope ?? '—')}
          </Descriptions.Item>
        </Descriptions>
      </Card>

      <Card
        title={
          <Space size={8}>
            <ShieldCheck size={16} />
            生效权限
          </Space>
        }
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
        {/* 这句话原先挂在卡片右上角。窄屏下它会与标题争同一行、把标题挤成省略号，
            所以改为一律放在内容之后的说明行。 */}
        <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
          由服务端展开，前端不做推导
        </Typography.Paragraph>
      </Card>
    </Space>
  )
}
