import { Layout, Menu, Space, Tag, Typography, Button, Input } from 'antd'
import { useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'

import { useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'

const { Header, Sider, Content } = Layout

/**
 * 应用外壳。
 *
 * 菜单项按权限裁剪——无权限的入口**不渲染**而不是置灰，
 * 避免导航栏被大量无权项占据。
 */
export function AppLayout(): React.ReactNode {
  const navigate = useNavigate()
  const location = useLocation()
  const { subject, scope, setScope, signOut } = useSession()
  const canReadRoles = useAnyPermission([PermissionCodes.RbacRoleRead])

  const [scopeDraft, setScopeDraft] = useState(scope)

  const items = [
    { key: '/', label: '概览' },
    ...(canReadRoles ? [{ key: '/roles', label: '角色' }] : []),
  ]

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
        <Space style={{ marginLeft: 'auto' }}>
          <Input
            value={scopeDraft}
            onChange={(e) => setScopeDraft(e.target.value)}
            onBlur={() => void applyScope()}
            onPressEnter={() => void applyScope()}
            placeholder="作用域，如 tenant/acme"
            style={{ width: 220 }}
            aria-label="当前作用域"
          />
          <Tag>{subject?.subjectId ?? '未登录'}</Tag>
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
