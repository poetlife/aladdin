import { Drawer, Layout, theme } from 'antd'
import { Menu as MenuIcon, PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { useState } from 'react'
import { Outlet } from 'react-router-dom'

import { ProfileProvider } from '../profile'
import { ScopesProvider } from '../rbac'
import { AppHeader } from './AppHeader'
import { AppSidebar } from './AppSidebar'
import { useNarrowViewport } from './use-narrow-viewport'

const { Sider, Content } = Layout

const TOGGLE_ICON_SIZE = 18

/**
 * 应用外壳。
 *
 * 外壳本身只要**已认证**：零权限的主体也看得到它，界面是空的。
 * 见 router.tsx 的两层准入。
 *
 * 宽窄两态：宽屏是常驻侧边栏（可收成导轨），窄屏换成抽屉。
 * 两者承载的是同一个 AppSidebar，见 docs/design/web/README.md 的「响应式与窄屏」。
 */
export function AppLayout(): React.ReactNode {
  // 档案由外壳持有：账号区要显示展示名，而档案页要改它。放在这里，
  // 两者读的是同一份状态，改完之后账号区立刻跟着变。
  //
  // 范围目录同理：顶栏要拿它当候选，而范围页要增删它。放在外壳上，
  // 刚登记的范围立刻出现在候选里，而不是等下一次整页刷新。
  return (
    <ProfileProvider>
      <ScopesProvider>
        <AppShell />
      </ScopesProvider>
    </ProfileProvider>
  )
}

function AppShell(): React.ReactNode {
  const { token } = theme.useToken()
  const narrow = useNarrowViewport()

  const [collapsed, setCollapsed] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)

  const closeDrawer = (): void => setDrawerOpen(false)

  // 页头那个开关的图标、无障碍标签与行为都在这里定：页头只负责画出来。
  // 宽屏管的是导轨的收放，窄屏管的是抽屉的开合——同一颗按钮，两种语义。
  const toggle = narrow
    ? { icon: <MenuIcon size={TOGGLE_ICON_SIZE} />, label: '打开导航', onClick: () => setDrawerOpen(true) }
    : collapsed
      ? { icon: <PanelLeftOpen size={TOGGLE_ICON_SIZE} />, label: '展开侧边栏', onClick: () => setCollapsed(false) }
      : { icon: <PanelLeftClose size={TOGGLE_ICON_SIZE} />, label: '收起侧边栏', onClick: () => setCollapsed(true) }

  return (
    // 外壳占满视口且**不随内容变高**：内容超出时由内容区自己滚动。
    // 若这里是 minHeight，页面一长整份文档就变高，侧边栏跟着被撑长，
    // 钉底的账号区就跑到文档底部去了——那正是滚动它就会跟着消失的原因。
    // 用 dvh 而非 vh：移动端浏览器收起地址栏时 vh 不会跟着变，底部会被切掉一截。
    <Layout style={{ height: '100dvh' }}>
      {narrow ? (
        // 窄屏不渲染侧边栏：64px 的导轨手机上也占着地方，等于白白压窄内容区。
        // 抽屉的内容与 Sider 是同一个组件，因此不出现第二份导航。
        <Drawer
          placement="left"
          size={280}
          open={drawerOpen}
          onClose={closeDrawer}
          // 没有页头（无标题、无可关闭按钮）时 antd 不渲染抽屉头部，body 直接满高；
          // 再去掉 body 默认的 24px 内边距与自身滚动，让它和内层那份
          // `height:100%` 的三段列贴合——否则会多出内边距并套一层滚动条。
          closable={false}
          styles={{
            body: { padding: 0, overflow: 'hidden' },
            // 抽屉面板默认是 colorBgElevated，而 Sider theme="light" 用的是
            // colorBgContainer，暗色下两者有可见差别。对齐过来，仍是 token 取值。
            section: { background: token.colorBgContainer },
          }}
        >
          <AppSidebar collapsed={false} onNavigate={closeDrawer} onClose={closeDrawer} />
        </Drawer>
      ) : (
        // theme="light" 的底色是 colorBgContainer，它随明暗算法走——
        // 这里不写死色值，暗色下侧边栏自然比内容区更亮一层。
        //
        // 不设 breakpoint / collapsible / onCollapse：收放由上面那颗页头按钮直接驱动，
        // 而 trigger={null} 之下后两者本来也不会被调用，留着就是死属性。
        // 宽窄的判断只有一处——useNarrowViewport。
        <Sider theme="light" collapsed={collapsed} trigger={null} width={220} collapsedWidth={64}>
          <AppSidebar collapsed={collapsed} />
        </Sider>
      )}
      <Layout>
        <AppHeader toggleIcon={toggle.icon} toggleLabel={toggle.label} onToggle={toggle.onClick} />
        {/* 内容区是唯一的滚动容器：滚动它不会带走侧边栏底部的账号区。
            它自己撑满剩余高度靠的是 Layout 给的 flex，不需要再声明一次。
            窄屏把内边距收一收：手机上一页只有 360 出头，24px 的边一圈就吃掉一成多。 */}
        <Content style={{ padding: narrow ? 12 : 24, overflowY: 'auto' }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  )
}
