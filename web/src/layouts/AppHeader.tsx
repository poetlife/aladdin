import { Button, Input, Layout, theme } from 'antd'
import { Building2 } from 'lucide-react'
import { useState } from 'react'

import { useSession } from '../auth'
import { ThemeSwitch } from '../theme'

const { Header } = Layout

interface AppHeaderProps {
  /** 开关的图标。宽屏是收放导轨，窄屏是开抽屉——语义由外壳定，这里只管画。 */
  toggleIcon: React.ReactNode
  /** 开关的无障碍标签，同样由外壳给出。 */
  toggleLabel: string
  onToggle: () => void
}

/**
 * 页头：与**当前视图**相关的控件——折叠开关、作用域、主题。
 *
 * 账号入口不在这里，在侧边栏底部（见 AppSidebar.tsx）：它回答的是"我是谁"，
 * 与导航同类，因此与导航同处一侧。
 *
 * 那颗开关在宽屏与窄屏下做的事不同（收放导轨 / 开抽屉），页头不自己判断是哪一种，
 * 图标、标签与行为都由外壳注入——判断"是否窄屏"的地方只有一处。
 *
 * 背景与分隔线取自 `theme.useToken()`，而不是 antd 的 `Layout.Header` 默认值——
 * 后者是硬编码的深色 `#001529`，不随明暗算法变化，会在亮色主题下留一条深色带。
 */
export function AppHeader({ toggleIcon, toggleLabel, onToggle }: AppHeaderProps): React.ReactNode {
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
      <Button type="text" aria-label={toggleLabel} icon={toggleIcon} onClick={onToggle} />

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
          minWidth: 0,
        }}
      >
        {/* 宽度是弹性的：桌面端封顶 220，手机上有多少用多少。
            写死 220 会在 360px 的屏上把主题切换挤出页头。 */}
        <Input
          value={scopeDraft}
          onChange={(e) => setScopeDraft(e.target.value)}
          onBlur={() => void applyScope()}
          onPressEnter={() => void applyScope()}
          placeholder="作用域，如 tenant/acme"
          prefix={<Building2 size={14} />}
          style={{ flex: '1 1 160px', maxWidth: 220, minWidth: 0 }}
          aria-label="当前作用域"
        />

        <ThemeSwitch />
      </div>
    </Header>
  )
}
