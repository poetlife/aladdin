import { Card, Collapse, Descriptions, Empty, Skeleton, Space, Tag, Typography } from 'antd'
import { LayoutDashboard } from 'lucide-react'

import { usePermissionSet, useSession } from '../auth'
import { formatScope } from '../rbac'
import { useProfile } from '../profile'

/**
 * 首页：当前会话，以及（收在高级区里的）生效权限。
 *
 * 默认视图回答的是管理者最常问的两件事——**我在哪个范围、这个范围下我能做什么**。
 * 因此主体与范围在上半部分，权限码明细收进下面的展开区：那是排障时才需要的
 * 清单，常驻会把"范围"这件事淹掉（见 docs/design/rbac/management-ui.md）。
 *
 * 它也是排障时最有用的一页——"前端按钮消失"这类问题的第一步，
 * 就是展开这里确认权限码集合是否符合预期。
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
          <Descriptions.Item label="管理范围">{formatScope(scope)}</Descriptions.Item>
          <Descriptions.Item label="凭证默认作用域">
            {formatScope(subject?.defaultScope ?? '')}
          </Descriptions.Item>
        </Descriptions>
        {/* 这三样东西名字里都带"范围/域"，是这套系统里最容易串台的地方，
            因此把它们的区别写在数据旁边，而不是只写在设计文档里。 */}
        <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
          <Typography.Text strong>管理范围</Typography.Text>
          是这次判定的范围，可以在顶栏切换；
          <Typography.Text strong>凭证默认作用域</Typography.Text>
          在签发会话时就冻结了，只有"查我自己"这类方法按它判定。两者都与权限码里的「领域」段
          （如 rbac、galaxy）无关。
        </Typography.Paragraph>
      </Card>

      <Collapse
        items={[
          {
            key: 'permissions',
            label: `生效权限（${codes.length}）`,
            children:
              codes.length === 0 ? (
                <Empty
                  image={Empty.PRESENTED_IMAGE_SIMPLE}
                  description="当前管理范围下没有生效权限"
                />
              ) : (
                <>
                  <Space wrap>
                    {codes.map((code) => (
                      <Tag key={code}>{code}</Tag>
                    ))}
                  </Space>
                  <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
                    由服务端展开，前端不做推导。
                  </Typography.Paragraph>
                </>
              ),
          },
        ]}
      />
    </Space>
  )
}
