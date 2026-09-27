import { Button, Dropdown } from 'antd'
import type { MenuProps } from 'antd'
import { Monitor, Moon, Sun, type LucideIcon } from 'lucide-react'

import { useTheme } from './theme-context'
import type { ThemePreference } from './theme-preference'

const ICON_SIZE = 16

/**
 * 三档偏好各自的图标与文案。文案不能省：「跟随系统」与「亮色」的图标
 * 在小尺寸下分辨不出来，只留图标会让人靠猜。
 */
const OPTIONS: Record<ThemePreference, { icon: LucideIcon; label: string }> = {
  light: { icon: Sun, label: '亮色' },
  dark: { icon: Moon, label: '暗色' },
  system: { icon: Monitor, label: '跟随系统' },
}

/** 展开后的排列顺序：两个固定档在前，跟随系统在后。 */
const ORDER: readonly ThemePreference[] = ['light', 'dark', 'system']

/**
 * 主题切换。放在页头，位于 ConfigProvider 之内（它要用 antd 的控件）。
 *
 * 收成单个图标按钮：按钮上的图标就是当前档位，一眼可见；三档的完整选项
 * 展开才占宽度，页头在窄屏下不再为此多占一行。
 */
export function ThemeSwitch(): React.ReactNode {
  const { preference, setPreference } = useTheme()
  const { icon: CurrentIcon, label: currentLabel } = OPTIONS[preference]

  const items: MenuProps['items'] = ORDER.map((value) => {
    const { icon: Icon, label } = OPTIONS[value]
    return { key: value, label, icon: <Icon size={ICON_SIZE} /> }
  })

  return (
    <Dropdown
      placement="bottomRight"
      trigger={['click']}
      menu={{
        items,
        selectable: true,
        selectedKeys: [preference],
        onClick: ({ key }) => setPreference(key as ThemePreference),
      }}
    >
      <Button type="text" aria-label={`主题：${currentLabel}`} icon={<CurrentIcon size={18} />} />
    </Dropdown>
  )
}
