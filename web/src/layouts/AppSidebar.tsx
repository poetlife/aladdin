import { Avatar, Button, Dropdown, Menu, theme } from 'antd'
import type { MenuProps } from 'antd'
import {
  BookOpen,
  ChevronDown,
  CircleUserRound,
  Lamp,
  LayoutDashboard,
  LogOut,
  ShieldCheck,
  Sparkles,
  X,
} from 'lucide-react'
import { useLocation, useNavigate } from 'react-router-dom'

import { useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import { avatarFallbackInitial, useProfile } from '../profile'

const ICON_SIZE = 16

interface AppSidebarProps {
  /** 是否收起成导轨（只留图标）。宽屏的收起态用得上；抽屉里恒为展开。 */
  collapsed: boolean
  /** 选中一个导航项之后的回调。抽屉里用它把自己关上，宽屏不传。 */
  onNavigate?: () => void
  /** 给出时品牌行右侧渲染关闭按钮。抽屉里给，宽屏不给。 */
  onClose?: () => void
}

/**
 * 侧边栏的内容：品牌区 + 导航 + 账号区。
 *
 * 宽屏的 `Sider` 与窄屏的抽屉渲染的是**同一份**内容——两份就会各自演化，
 * 而"导航有哪几项、账号区长什么样"是同一件事。
 *
 * 菜单项按权限裁剪——无权限的入口**不渲染**而不是置灰，
 * 避免导航栏被大量无权项占据。
 */
export function AppSidebar({ collapsed, onNavigate, onClose }: AppSidebarProps): React.ReactNode {
  const navigate = useNavigate()
  const location = useLocation()
  const canReadRoles = useAnyPermission([PermissionCodes.RbacRoleRead])
  const canReadGalaxy = useAnyPermission([PermissionCodes.GalaxyProjectRead])

  // 侧边栏内部的跳转一律走这里：导航项与账号区共用，"跳转之后要通知外壳"
  // （窄屏下就是收起抽屉）因此只有一份实现。分成两处就会漏——账号区那一条
  // 就漏过：跳过去之后抽屉还盖在页面上。
  const go = (path: string): void => {
    void navigate(path)
    onNavigate?.()
  }

  const items = [
    { key: '/', label: '概览', icon: <LayoutDashboard size={ICON_SIZE} /> },
    // galaxy 创作入口：无 `galaxy.project.read` 时不渲染，导航栏不被无权项占据。
    ...(canReadGalaxy
      ? [{ key: '/galaxy', label: '创作', icon: <Sparkles size={ICON_SIZE} /> }]
      : []),
    // 个人资料不需要权限码：它只作用于自己（见 docs/design/profile/README.md）。
    { key: '/profile', label: '个人资料', icon: <CircleUserRound size={ICON_SIZE} /> },
    ...(canReadRoles
      ? [{ key: '/roles', label: '角色', icon: <ShieldCheck size={ICON_SIZE} /> }]
      : []),
    // 文档区也不需要权限码：它讲的是"怎么把命令行装上并登录"，
    // 而零权限的主体恰恰最需要它（见 docs/design/web/docs-area.md）。
    // 导航**只有这一项**：章节加页只往区域里加，这里不再变。
    { key: '/docs', label: '文档', icon: <BookOpen size={ICON_SIZE} /> },
  ]

  // 导航项都是一级路径，而页面可以更深（/docs/cli、/galaxy/<工程标识>）。
  // 拿整个路径去比对，进到子页时父项就不再高亮，因此取第一段。
  const selectedKey = `/${location.pathname.split('/')[1] ?? ''}`

  return (
    // 三段纵向排布：品牌区固定，导航占满剩余高度，账号区钉底。
    // 导航自己滚动，账号区因此始终留在视口内。
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <Brand collapsed={collapsed} onClose={onClose} />
      {/* 不传 inlineCollapsed：在 Sider 里它自己读 SiderContext 跟着收起，
          在抽屉里那份内容被 portal 到 SiderContext 之外，自然就是展开的——
          正是我们要的，不用手动分叉。 */}
      <Menu
        mode="inline"
        selectedKeys={[selectedKey]}
        items={items}
        onClick={({ key }) => {
          go(key)
        }}
        style={{ flex: 1, overflowY: 'auto' }}
      />
      <SidebarAccount collapsed={collapsed} onNavigate={go} />
    </div>
  )
}

/**
 * 侧边栏顶部的品牌区。
 *
 * 高度取自 `controlHeight * 2`——与 antd 的页头默认高度同一个算式，
 * 于是品牌区与页头始终齐平，中间那条分隔线是连续的一根。
 */
function Brand({
  collapsed,
  onClose,
}: {
  collapsed: boolean
  onClose?: (() => void) | undefined
}): React.ReactNode {
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
      {/* 抽屉没有页头也没有遮罩之外的收起入口，关掉它的按钮就落在品牌行这一端。
          宽屏不需要它：那时开关在页头，侧边栏本来就能收成导轨。 */}
      {onClose !== undefined && (
        <Button
          type="text"
          aria-label="关闭导航"
          icon={<X size={18} />}
          onClick={onClose}
          style={{ marginLeft: 'auto', flexShrink: 0 }}
        />
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
 *
 * 跳转走外面给的 `onNavigate`，与导航项同一条路：账号区也是"从侧边栏里跳走"，
 * 窄屏下同样要把抽屉收起来。
 */
function SidebarAccount({
  collapsed,
  onNavigate,
}: {
  collapsed: boolean
  onNavigate: (path: string) => void
}): React.ReactNode {
  const { token } = theme.useToken()
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
              onNavigate('/login')
              return
            }
            onNavigate(key)
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
