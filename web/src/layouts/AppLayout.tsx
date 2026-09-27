import { Avatar, Button, Dropdown, Layout, Menu, theme } from 'antd'
import type { MenuProps } from 'antd'
import { ChevronDown, CircleUserRound, Lamp, LayoutDashboard, LogOut, ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'

import { useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import { avatarFallbackInitial, ProfileProvider, useProfile } from '../profile'
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
  // 档案由外壳持有：账号区要显示展示名，而档案页要改它。放在这里，
  // 两者读的是同一份状态，改完之后账号区立刻跟着变。
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
    // 外壳占满视口且**不随内容变高**：内容超出时由内容区自己滚动。
    // 若这里是 minHeight，页面一长整份文档就变高，侧边栏跟着被撑长，
    // 钉底的账号区就跑到文档底部去了——那正是滚动它就会跟着消失的原因。
    // 用 dvh 而非 vh：移动端浏览器收起地址栏时 vh 不会跟着变，底部会被切掉一截。
    <Layout style={{ height: '100dvh' }}>
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
        {/* 三段纵向排布：品牌区固定，导航占满剩余高度，账号区钉底。
            导航自己滚动，账号区因此始终留在视口内。 */}
        <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
          <Brand collapsed={collapsed} />
          <Menu
            mode="inline"
            selectedKeys={[location.pathname]}
            items={items}
            onClick={({ key }) => void navigate(key)}
            style={{ flex: 1, overflowY: 'auto' }}
          />
          <SidebarAccount collapsed={collapsed} />
        </div>
      </Sider>
      <Layout>
        <AppHeader collapsed={collapsed} onToggleCollapsed={() => setCollapsed((prev) => !prev)} />
        {/* 内容区是唯一的滚动容器：滚动它不会带走侧边栏底部的账号区。
            它自己撑满剩余高度靠的是 Layout 给的 flex，不需要再声明一次。 */}
        <Content style={{ padding: 24, overflowY: 'auto' }}>
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

/**
 * 侧边栏底部的账号区：头像、展示名，点开是个人资料与退出登录。
 *
 * 它放在这里而不是页头：账号与导航回答的是同一类问题——"我是谁、要去哪一页"，
 * 因此同处一侧；页头留给与当前视图相关的控件（折叠、作用域、主题）。
 * 侧边栏收起时只留头像，展示名靠 `title` 与下拉菜单表达。
 */
function SidebarAccount({ collapsed }: { collapsed: boolean }): React.ReactNode {
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const { subject, signOut } = useSession()
  const { profile } = useProfile()

  // 展示名由服务端算好（未设昵称时回退到渠道标识）。它还没到时先显示主体
  // 标识——那正是回退规则的最后一档，因此不是一个"错的中间态"，
  // 只是暂时停在了最后一档。
  const displayName = profile?.displayName ?? subject?.subjectId ?? '未登录'
  const avatarUrl = profile?.avatarUrl ?? ''

  const items: MenuProps['items'] = [
    { key: '/profile', label: '个人资料', icon: <CircleUserRound size={ICON_SIZE} /> },
    { type: 'divider' },
    { key: 'sign-out', label: '退出登录', icon: <LogOut size={ICON_SIZE} />, danger: true },
  ]

  return (
    <div style={{ borderTop: `1px solid ${token.colorBorderSecondary}`, padding: 8 }}>
      <Dropdown
        placement="topLeft"
        trigger={['click']}
        menu={{
          items,
          onClick: ({ key }) => {
            if (key === 'sign-out') {
              signOut()
              void navigate('/login')
              return
            }
            void navigate(key)
          },
        }}
      >
        {/* 内容包在自己这一层 flex 里，而不是让 Button 自己当 flex 容器：
            antd 会把 Button 的子节点再包进一个块级 span，那个包裹层按内容撑开，
            于是里面的元素拿不到可用宽度，展示名不会被截断。 */}
        <Button type="text" title={displayName} style={{ width: '100%', padding: 6 }}>
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: collapsed ? 'center' : 'flex-start',
              gap: 8,
              minWidth: 0,
            }}
          >
            <Avatar size="small" src={avatarUrl === '' ? undefined : avatarUrl}>
              {avatarFallbackInitial(displayName)}
            </Avatar>
            {!collapsed && (
              <>
                <span
                  style={{
                    flex: 1,
                    minWidth: 0,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    textAlign: 'left',
                  }}
                >
                  {displayName}
                </span>
                <ChevronDown size={14} style={{ flexShrink: 0, opacity: 0.6 }} />
              </>
            )}
          </div>
        </Button>
      </Dropdown>
    </div>
  )
}
