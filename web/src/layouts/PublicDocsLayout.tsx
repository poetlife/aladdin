import { Layout, Space, theme } from 'antd'
import { Home } from 'lucide-react'
import { Link, NavLink, Outlet } from 'react-router-dom'

import { CHAPTERS } from '../pages/docs/chapters/manifest'
import { ThemeSwitch } from '../theme'
import { BrandMark } from '../ui/BrandMark'

const { Content, Header } = Layout

/** 正文的最大宽度。超过它的行一眼扫不回来，而文档是拿来读的。 */
const MAX_CONTENT_WIDTH = 880

/**
 * 文档区的公开外壳。
 *
 * 这是"放出去"那条路线上的那一层（见 docs/design/web/docs-area.md）：文档区不经
 * `RequirePermission`，因此也不经 `AppLayout`，于是需要**它自己的一层外壳**——
 * 品牌区 + 文档导航 + 主题切换，**没有账号区、没有管理范围**。那两样都回答
 * "我是谁、我在哪个范围内工作"，而访客没有这两样东西。
 *
 * 它仍然与前端的其余部分同源：同一套 token、同一个 `BrandMark`、同一个
 * `ThemeSwitch`。因此换主题时它跟着换——静态站做不到这一点，而那正是文档区
 * 不放在 `web/public/` 下的理由。
 *
 * **不用 `useNarrowViewport()`**：它读 `matchMedia`，在预渲染（Node 里没有
 * `matchMedia`）与浏览器里会得到不同结论，于是静态 HTML 与水合后的第一帧结构
 * 对不上。窄屏这里交给 `flexWrap`，那件事交给 CSS 判。
 */
export function PublicDocsLayout(): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <Layout style={{ minHeight: '100dvh', background: token.colorBgLayout }}>
      <Header
        style={{
          height: 'auto',
          lineHeight: 1.5,
          padding: `${token.paddingSM}px ${token.padding}px`,
          background: token.colorBgContainer,
          borderBottom: `${token.lineWidth}px solid ${token.colorBorderSecondary}`,
        }}
      >
        <div
          style={{
            maxWidth: MAX_CONTENT_WIDTH,
            margin: '0 auto',
            display: 'flex',
            alignItems: 'center',
            flexWrap: 'wrap',
            gap: token.marginSM,
          }}
        >
          <Link
            to="/docs"
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              color: token.colorText,
              fontWeight: token.fontWeightStrong,
            }}
          >
            <BrandMark size={20} />
            文档
          </Link>

          <nav style={{ display: 'flex', flexWrap: 'wrap', gap: token.marginSM }}>
            {CHAPTERS.map((chapter) => (
              <NavLink
                key={chapter.slug}
                to={`/docs/${chapter.slug}`}
                style={({ isActive }) => ({
                  color: isActive ? token.colorPrimary : token.colorTextSecondary,
                })}
              >
                {chapter.title}
              </NavLink>
            ))}
          </nav>

          {/* 工具区推到右边。窄屏换行之后它自己占一行。 */}
          <Space size={token.marginSM} style={{ marginInlineStart: 'auto' }}>
            <ThemeSwitch />
            {/* 登录用户从侧边栏点进来也会落到这层外壳上，于是"怎么回去"必须写在
                这里——否则他只能按浏览器后退，而那是浏览器的功能，不是这一页的。 */}
            <Link
              to="/"
              style={{ display: 'flex', alignItems: 'center', gap: 6, color: token.colorTextSecondary }}
            >
              <Home size={16} />
              进入应用
            </Link>
          </Space>
        </div>
      </Header>

      <Content style={{ padding: token.padding }}>
        <div style={{ maxWidth: MAX_CONTENT_WIDTH, margin: '0 auto' }}>
          <Outlet />
        </div>
      </Content>
    </Layout>
  )
}
