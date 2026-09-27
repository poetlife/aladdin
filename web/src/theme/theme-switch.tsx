import { Segmented } from 'antd'
import { Monitor, Moon, Sun } from 'lucide-react'

import { useTheme } from './theme-context'
import type { ThemePreference } from './theme-preference'

const ICON_SIZE = 14

/**
 * 三档选项。图标 + 文案都给出：「跟随系统」与「亮色」在小尺寸图标上
 * 分辨不出来，只留图标会让人靠猜。
 */
const OPTIONS = [
  { value: 'light' as const, icon: <Sun size={ICON_SIZE} />, label: '亮色' },
  { value: 'dark' as const, icon: <Moon size={ICON_SIZE} />, label: '暗色' },
  { value: 'system' as const, icon: <Monitor size={ICON_SIZE} />, label: '跟随系统' },
]

/**
 * 主题切换。放在页头，位于 ConfigProvider 之内（它要用 antd 的控件）。
 */
export function ThemeSwitch(): React.ReactNode {
  const { preference, setPreference } = useTheme()

  return (
    <Segmented<ThemePreference>
      size="small"
      options={OPTIONS}
      value={preference}
      onChange={setPreference}
      aria-label="主题"
    />
  )
}
