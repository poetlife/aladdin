import { Button, Input, Layout, theme } from 'antd'
import { Building2, PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { useState } from 'react'

import { useSession } from '../auth'
import { ThemeSwitch } from '../theme'

const { Header } = Layout

interface AppHeaderProps {
  /** 侧边栏当前是否收起。 */
  collapsed: boolean
  /** 切换侧边栏收起状态。 */
  onToggleCollapsed: () => void
}

/**
 * 页头：与**当前视图**相关的控件——折叠开关、作用域、主题。
 *
 * 账号入口不在这里，在侧边栏底部（见 AppLayout.tsx）：它回答的是"我是谁"，
 * 与导航同类，因此与导航同处一侧。
 *
 * 背景与分隔线取自 `theme.useToken()`，而不是 antd 的 `Layout.Header` 默认值——
 * 后者是硬编码的深色 `#001529`，不随明暗算法变化，会在亮色主题下留一条深色带。
 */
export function AppHeader({ collapsed, onToggleCollapsed }: AppHeaderProps): React.ReactNode {
  const { token } = theme.useToken()
  const { scope, setScope } = useSession()

  const [scopeDraft, setScopeDraft] = useState(scope)

  async function applyScope(): Promise<void> {
    if (scopeDraft !== scope) {
      await setScope(scopeDraft)
    }
  }

  return (
    <Header
      style={{
        // 页头默认是等高单行（height / line-height 都取自 headerHeight），
        // 窄屏下这一行装不下工具区。改成等高不固定、允许换行，
        // 桌面端靠 minHeight 与侧边栏的品牌区齐平，窄屏则多占一行而不是把控件挤出去。
        height: 'auto',
        minHeight: token.controlHeight * 2,
        lineHeight: 1.5,
        padding: '8px 16px',
        display: 'flex',
        alignItems: 'center',
        flexWrap: 'wrap',
        gap: 12,
        background: token.colorBgContainer,
        borderBottom: `1px solid ${token.colorBorderSecondary}`,
      }}
    >
      <Button
        type="text"
        aria-label={collapsed ? '展开侧边栏' : '收起侧边栏'}
        icon={collapsed ? <PanelLeftOpen size={18} /> : <PanelLeftClose size={18} />}
        onClick={onToggleCollapsed}
      />

      {/* 工具区自己也是一个可换行的 flex 容器，而不是一个整体：
          Space 是一整个 flex 项，窄屏下它会整体溢出到页头之外（控件被裁掉），
          换成容器后每个控件各自找位置，只会多占一行。 */}
      <div
        style={{
          marginLeft: 'auto',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'flex-end',
          flexWrap: 'wrap',
          gap: 12,
        }}
      >
        <Input
          value={scopeDraft}
          onChange={(e) => setScopeDraft(e.target.value)}
          onBlur={() => void applyScope()}
          onPressEnter={() => void applyScope()}
          placeholder="作用域，如 tenant/acme"
          prefix={<Building2 size={14} />}
          style={{ width: 220 }}
          aria-label="当前作用域"
        />

        <ThemeSwitch />
      </div>
    </Header>
  )
}
