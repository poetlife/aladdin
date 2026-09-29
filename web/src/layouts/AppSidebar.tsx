import { Avatar, Button, Dropdown, Menu, theme } from 'antd'
import type { MenuProps } from 'antd'
import {
  BookOpen,
  ChevronDown,
  CircleUserRound,
  KeyRound,
  Lamp,
  LayoutDashboard,
  LogOut,
  ShieldCheck,
  Sparkles,
  UserCog,
  X,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

import { usePermissionSet, useSession } from '../auth'
import type { PermissionCode } from '../gen/permission-codes'
import { PermissionCodes } from '../gen/permission-codes'
import { avatarFallbackInitial, useProfile } from '../profile'

const ICON_SIZE = 16

/** 导航分组的标识。分组本身不指向任何页面，只是一个可展开的容器。 */
const GROUP_ACCESS = 'access'

/** 分组的显示信息。 */
const GROUP_LABELS: Record<string, { label: string; icon: React.ReactNode }> = {
  [GROUP_ACCESS]: { label: '权限', icon: <ShieldCheck size={ICON_SIZE} /> },
}

/**
 * 一个导航项。
 *
 * 声明在一处：路径、标签、图标、所属分组、以及**看到它所需的权限**。
 * 菜单树、选中态与展开态都从这份声明派生，免得"有哪几项""哪几项算叶子"
 * 在几处各写一份——那正是导航最容易漂的地方。
 */
interface NavEntry {
  /** 路由路径，同时是菜单项的 key。 */
  path: string
  label: string
  icon: React.ReactNode
  /** 所属分组；留空即一级项。 */
  group?: string
  /**
   * 渲染这一项所需的权限码；留空表示任何已认证主体都能看到。
   * 它只是展示裁剪，不是安全边界（见 docs/design/rbac/frontend-permissions.md）。
   */
  permission?: PermissionCode
}

const NAV: NavEntry[] = [
  { path: '/', label: '概览', icon: <LayoutDashboard size={ICON_SIZE} /> },
  { path: '/galaxy', label: '创作', icon: <Sparkles size={ICON_SIZE} />, permission: PermissionCodes.GalaxyProjectRead },
  // 个人资料不需要权限码：它只作用于自己（见 docs/design/profile/README.md）。
  { path: '/profile', label: '个人资料', icon: <CircleUserRound size={ICON_SIZE} /> },
  // 权限管理这一组：角色定义是"有哪些角色"，人员授权是"谁被授了哪个角色"。
  // 两者同属一件事，因此收在一个分组里，而不是平铺成两个看不出关系的入口。
  { path: '/access/roles', label: '角色定义', icon: <ShieldCheck size={ICON_SIZE} />, group: GROUP_ACCESS, permission: PermissionCodes.RbacRoleRead },
  { path: '/access/subjects', label: '人员授权', icon: <UserCog size={ICON_SIZE} />, group: GROUP_ACCESS, permission: PermissionCodes.RbacSubjectRead },
  // 权限码目录是只读对照（码 + 说明 + 直接声明它的角色），与角色定义同一道权限门。
  { path: '/access/codes', label: '权限码', icon: <KeyRound size={ICON_SIZE} />, group: GROUP_ACCESS, permission: PermissionCodes.RbacRoleRead },
  // 文档区也不需要权限码：它讲的是"怎么把命令行装上并登录"，
  // 而零权限的主体恰恰最需要它（见 docs/design/web/docs-area.md）。
  // 导航**只有这一项**：章节加页只往区域里加，这里不再变。
  { path: '/docs', label: '文档', icon: <BookOpen size={ICON_SIZE} /> },
]

/**
 * 路径是否落在这一项上。
 *
 * 不能只比路径本身：页面可以更深（`/docs/cli`、`/galaxy/<工程标识>`），
 * 进到子页时父项仍要高亮。
 */
function matchesPath(path: string, pathname: string): boolean {
  if (path === '/') return pathname === '/'
  return pathname === path || pathname.startsWith(`${path}/`)
}

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
  const permissions = usePermissionSet()

  // 侧边栏内部的跳转一律走这里：导航项与账号区共用，"跳转之后要通知外壳"
  // （窄屏下就是收起抽屉）因此只有一份实现。分成两处就会漏——账号区那一条
  // 就漏过：跳过去之后抽屉还盖在页面上。
  const go = (path: string): void => {
    void navigate(path)
    onNavigate?.()
  }

  const visible = NAV.filter(
    (entry) => entry.permission === undefined || permissions.hasAny([entry.permission]),
  )

  // 菜单树按声明顺序生成；分组在它的**第一个可见子项**处插入，
  // 这样"权限"这一组出现在声明里那个位置，而不是被排到最前或最后。
  const items: MenuProps['items'] = []
  const emitted = new Set<string>()
  for (const entry of visible) {
    if (entry.group === undefined) {
      items.push({ key: entry.path, label: entry.label, icon: entry.icon })
      continue
    }
    if (emitted.has(entry.group)) continue
    emitted.add(entry.group)
    const children = visible.filter((e) => e.group === entry.group)
    const group = GROUP_LABELS[entry.group]
    items.push({
      key: entry.group,
      label: group?.label ?? entry.group,
      icon: group?.icon,
      children: children.map((e) => ({ key: e.path, label: e.label, icon: e.icon })),
    })
  }

  // 选中态取**最长匹配**的那一项。原先取路径第一段，那是因为导航是扁平的；
  // 有了分组之后，第一段是分组键（如 /access），会把分组本身选成选中项，
  // 而分组不是页面。
  const selectedKey =
    visible
      .filter((entry) => matchesPath(entry.path, location.pathname))
      .sort((a, b) => b.path.length - a.path.length)[0]?.path ?? ''

  // 深链或刷新进来时，选中项所在的分组必须**已经是展开的**——否则选中项
  // 藏在收起的分组里，等于没被选中。
  const activeGroup = visible.find((entry) => entry.path === selectedKey)?.group
  const [openKeys, setOpenKeys] = useState<string[]>(activeGroup === undefined ? [] : [activeGroup])
  useEffect(() => {
    if (activeGroup === undefined) return
    setOpenKeys((prev) => (prev.includes(activeGroup) ? prev : [...prev, activeGroup]))
  }, [activeGroup])

  return (
    // 三段纵向排布：品牌区固定，导航占满剩余高度，账号区钉底。
    // 导航自己滚动，账号区因此始终留在视口内。
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <Brand collapsed={collapsed} onClose={onClose} />
      {/* 不传 inlineCollapsed：在 Sider 里它自己读 SiderContext 跟着收起，
          在抽屉里那份内容被 portal 到 SiderContext 之外，自然就是展开的——
          正是我们要的，不用手动分叉。

          展开态只在**没收起**时受控：导轨态下 antd 把子菜单换成悬停弹出，
          此时再塞一个 openKeys 会与它的悬停逻辑打架（子菜单弹不出来）。 */}
      <Menu
        mode="inline"
        selectedKeys={[selectedKey]}
        items={items}
        {...(collapsed ? {} : { openKeys, onOpenChange: setOpenKeys })}
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
