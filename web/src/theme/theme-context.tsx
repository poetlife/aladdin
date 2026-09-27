import { createContext, useContext, useEffect, useMemo, useState } from 'react'

import {
  DARK_MEDIA_QUERY,
  readThemePreference,
  resolveTheme,
  systemPrefersDark,
  writeThemePreference,
  type ResolvedTheme,
  type ThemePreference,
} from './theme-preference'

interface ThemeContextValue {
  /** 用户选的那一档（可能是「跟随系统」）。 */
  preference: ThemePreference
  /** 本次实际生效的主题，已把「跟随系统」折算掉。 */
  resolved: ResolvedTheme
  /** 切换偏好并落盘。 */
  setPreference: (next: ThemePreference) => void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

/**
 * 主题的持有者。
 *
 * 它在 ConfigProvider **之外**：明暗算法是由这里算出来交给 ConfigProvider 的，
 * 而不是反过来。因此本组件刻意不渲染任何 antd 组件——它只是 context，
 * 用到 antd 的切换控件在 ConfigProvider 内部（见 theme-switch.tsx）。
 *
 * 偏好只是呈现输入，不参与任何权限判定（见 CLAUDE.md 第 7 条）。
 */
export function ThemeProvider({ children }: { children: React.ReactNode }): React.ReactNode {
  // 初值同步读：放到副作用里读会让首帧按「跟随系统」渲染，选过固定主题的人
  // 会看到一瞬的另一种配色。
  const [preference, setPreferenceState] = useState<ThemePreference>(readThemePreference)
  const [prefersDark, setPrefersDark] = useState<boolean>(systemPrefersDark)

  // 系统偏好要订阅而不是每次渲染现查：用户在操作系统里切换时，
  // 「跟随系统」这一档必须跟着变，而 React 不会因为这个变化重渲染。
  useEffect(() => {
    const media = globalThis.matchMedia?.(DARK_MEDIA_QUERY)
    if (media === undefined) {
      return
    }
    const onChange = (event: MediaQueryListEvent): void => setPrefersDark(event.matches)
    media.addEventListener('change', onChange)
    // 订阅这一刻对齐一次：初值读取与订阅之间系统可能已经变过。
    setPrefersDark(media.matches)
    return () => media.removeEventListener('change', onChange)
  }, [])

  const resolved = resolveTheme(preference, prefersDark)

  // color-scheme 交给浏览器，原生滚动条与表单控件才会跟着换色；
  // antd 只管它自己渲染出来的那部分。
  useEffect(() => {
    document.documentElement.style.colorScheme = resolved
  }, [resolved])

  const value = useMemo<ThemeContextValue>(
    () => ({
      preference,
      resolved,
      setPreference: (next: ThemePreference) => {
        setPreferenceState(next)
        writeThemePreference(next)
      },
    }),
    [preference, resolved],
  )

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

/** 读取当前主题偏好与生效主题。必须在 ThemeProvider 之内使用。 */
export function useTheme(): ThemeContextValue {
  const value = useContext(ThemeContext)
  if (value === null) {
    throw new Error('useTheme 必须在 ThemeProvider 之内使用')
  }
  return value
}
