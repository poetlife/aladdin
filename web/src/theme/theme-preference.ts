/** 用户对界面主题的偏好：跟随系统，或固定为亮色 / 暗色。 */
export type ThemePreference = 'system' | 'light' | 'dark'

/** 解析后实际生效的主题——「跟随系统」在这一步被折算成两者之一。 */
export type ResolvedTheme = 'light' | 'dark'

/** 主题偏好在 localStorage 中的键名。 */
const THEME_STORAGE_KEY = 'aladdin.theme'

/** 系统偏好是否为暗色的媒体查询。订阅处与读取处共用同一串。 */
export const DARK_MEDIA_QUERY = '(prefers-color-scheme: dark)'

/** 三档偏好的合法取值，用来识别落盘值是否已经过期或被手改。 */
const PREFERENCES: readonly string[] = ['system', 'light', 'dark']

/**
 * 读取已保存的主题偏好。
 *
 * 存的是一个用户界面偏好，不是任何判定的输入：读不到、或读到不认识的值
 * （改动过、手改过、来自更老的版本）时一律回退到「跟随系统」，
 * 而不是让界面渲染不出来。
 */
export function readThemePreference(): ThemePreference {
  const raw = globalThis.localStorage?.getItem(THEME_STORAGE_KEY) ?? null
  return raw !== null && PREFERENCES.includes(raw) ? (raw as ThemePreference) : 'system'
}

/** 落盘主题偏好。存储不可用（隐私模式等）时静默跳过：它只影响下次打开时的观感。 */
export function writeThemePreference(preference: ThemePreference): void {
  globalThis.localStorage?.setItem(THEME_STORAGE_KEY, preference)
}

/**
 * 把三档偏好折算成本次实际生效的主题。
 *
 * 系统偏好由调用方传入而不是在这里现查：现查会让本函数依赖时刻，
 * 而调用方需要的是"媒体查询当前的状态"这一份可被订阅的值。
 */
export function resolveTheme(preference: ThemePreference, systemPrefersDark: boolean): ResolvedTheme {
  if (preference === 'system') {
    return systemPrefersDark ? 'dark' : 'light'
  }
  return preference
}

/** 查询系统当前的明暗偏好。浏览器不支持该媒体查询时按亮色处理。 */
export function systemPrefersDark(): boolean {
  return globalThis.matchMedia?.(DARK_MEDIA_QUERY).matches ?? false
}
