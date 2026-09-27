import { Layout, Menu, theme } from 'antd'
import { CircleUserRound, Lamp, LayoutDashboard, ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'

import { useAnyPermission } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import { ProfileProvider } from '../profile'
import { AppHeader } from './AppHeader'

const { Sider, Content } = Layout

const ICON_SIZE = 16

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
  const canReadRoles = useAnyPermission([PermissionCodes.RbacRoleRead])

  const [collapsed, setCollapsed] = useState(false)

  const items = [
    { key: '/', label: '概览', icon: <LayoutDashboard size={ICON_SIZE} /> },
    // 个人资料不需要权限码：它只作用于自己（见 docs/design/profile/README.md）。
    { key: '/profile', label: '个人资料', icon: <CircleUserRound size={ICON_SIZE} /> },
    ...(canReadRoles
      ? [{ key: '/roles', label: '角色', icon: <ShieldCheck size={ICON_SIZE} /> }]
      : []),
  ]

  return (
    <Layout style={{ minHeight: '100vh' }}>
      {/* theme="light" 的底色是 colorBgContainer，它随明暗算法走——
          这里不写死色值，暗色下侧边栏自然比内容区更亮一层。 */}
      <Sider
        theme="light"
        collapsible
        collapsed={collapsed}
        onCollapse={setCollapsed}
        trigger={null}
        breakpoint="lg"
        width={220}
        collapsedWidth={64}
      >
        <Brand collapsed={collapsed} />
        <Menu
          mode="inline"
          selectedKeys={[location.pathname]}
          items={items}
          onClick={({ key }) => void navigate(key)}
        />
      </Sider>
      <Layout>
        <AppHeader collapsed={collapsed} onToggleCollapsed={() => setCollapsed((prev) => !prev)} />
        <Content style={{ padding: 24 }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  )
}

/**
 * 侧边栏顶部的品牌区。
 *
 * 高度取自 `controlHeight * 2`——与 antd 的页头默认高度同一个算式，
 * 于是品牌区与页头始终齐平，中间那条分隔线是连续的一根。
 */
function Brand({ collapsed }: { collapsed: boolean }): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <div
      style={{
        height: token.controlHeight * 2,
        display: 'flex',
        alignItems: 'center',
        justifyContent: collapsed ? 'center' : 'flex-start',
        gap: 8,
        paddingInline: collapsed ? 0 : 20,
        borderBottom: `1px solid ${token.colorBorderSecondary}`,
        overflow: 'hidden',
      }}
    >
      <Lamp size={20} color={token.colorPrimary} />
      {!collapsed && (
        <span style={{ fontSize: 16, fontWeight: 600, whiteSpace: 'nowrap' }}>阿拉丁神灯</span>
      )}
    </div>
  )
}
