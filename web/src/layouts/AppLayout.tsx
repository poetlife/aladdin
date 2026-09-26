import { Avatar, Layout, Menu, Space, Tag, Typography, Button, Input } from 'antd'
import { useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'

import { useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import { ProfileProvider, avatarFallbackInitial, useProfile } from '../profile'

const { Header, Sider, Content } = Layout

/**
 * 应用外壳。
 *
 * 菜单项按权限裁剪——无权限的入口**不渲染**而不是置灰，
 * 避免导航栏被大量无权项占据。
 *
 * 外壳本身只要**已认证**：零权限的主体也看得到它，界面是空的。
 * 见 router.tsx 的两层准入。
 */
export function AppLayout(): React.ReactNode {
  // 档案由外壳持有：页头要显示展示名，而档案页要改它。放在这里，
  // 两者读的是同一份状态，改完之后页头立刻跟着变。
  return (
    <ProfileProvider>
      <AppShell />
    </ProfileProvider>
  )
}

function AppShell(): React.ReactNode {
  const navigate = useNavigate()
  const location = useLocation()
  const { subject, scope, setScope, signOut } = useSession()
  const { profile } = useProfile()
  const canReadRoles = useAnyPermission([PermissionCodes.RbacRoleRead])

  const [scopeDraft, setScopeDraft] = useState(scope)

  const items = [
    { key: '/', label: '概览' },
    // 个人资料不需要权限码：它只作用于自己（见 docs/design/profile/README.md）。
    { key: '/profile', label: '个人资料' },
    ...(canReadRoles ? [{ key: '/roles', label: '角色' }] : []),
  ]

  // 展示名由服务端算好（未设昵称时回退到渠道标识）。它还没到时先显示主体
  // 标识——那正是回退规则的最后一档，因此不是一个"错的中间态"，
  // 只是暂时停在了最后一档。
  const displayName = profile?.displayName ?? subject?.subjectId ?? '未登录'
  const avatarUrl = profile?.avatarUrl ?? ''

  async function applyScope(): Promise<void> {
    if (scopeDraft !== scope) {
      await setScope(scopeDraft)
    }
  }

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
        <Typography.Title level={4} style={{ color: '#fff', margin: 0, whiteSpace: 'nowrap' }}>
          阿拉丁神灯
        </Typography.Title>
        <Space style={{ marginLeft: 'auto' }} align="center">
          <Input
            value={scopeDraft}
            onChange={(e) => setScopeDraft(e.target.value)}
            onBlur={() => void applyScope()}
            onPressEnter={() => void applyScope()}
            placeholder="作用域，如 tenant/acme"
            style={{ width: 220 }}
            aria-label="当前作用域"
          />
          <Space size="small" align="center">
            <Avatar size="small" src={avatarUrl === '' ? undefined : avatarUrl}>
              {avatarFallbackInitial(displayName)}
            </Avatar>
            <Tag>{displayName}</Tag>
          </Space>
          <Button
            size="small"
            onClick={() => {
              signOut()
              void navigate('/login')
            }}
          >
            退出
          </Button>
        </Space>
      </Header>
      <Layout>
        <Sider width={180} theme="light">
          <Menu
            mode="inline"
            selectedKeys={[location.pathname]}
            items={items}
            onClick={({ key }) => void navigate(key)}
            style={{ height: '100%', borderInlineEnd: 0 }}
          />
        </Sider>
        <Content style={{ padding: 24 }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  )
}
