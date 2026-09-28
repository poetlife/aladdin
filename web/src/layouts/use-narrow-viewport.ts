import { useSyncExternalStore } from 'react'

/**
 * 窄屏的唯一定义。
 *
 * 取 antd 的 `lg`，写法与 antd Sider 自身拼查询串的方式一致
 * （`screen and (max-width: ${dimensionMaxMap[breakpoint]})`，见 antd 的 layout/Sider.js），
 * 于是"外壳换成抽屉"的切换点与 antd 认定的 `lg` 完全相同。
 */
export const NARROW_MEDIA_QUERY = 'screen and (max-width: 991.98px)'

function subscribe(onStoreChange: () => void): () => void {
  const media = window.matchMedia(NARROW_MEDIA_QUERY)
  media.addEventListener('change', onStoreChange)
  return () => media.removeEventListener('change', onStoreChange)
}

// 每次现取，不在模块顶层缓存 MediaQueryList：缓存之后测试就换不掉
// `window.matchMedia`，而这个函数的可测性正来自"它每次都重新问一遍"。
function getSnapshot(): boolean {
  return window.matchMedia(NARROW_MEDIA_QUERY).matches
}

/**
 * 当前视口是否窄屏（手机）。
 *
 * 全仓库"是否窄屏"的唯一判断入口（见 docs/ssot-registry.md）：需要按宽窄分叉时用它，
 * 不要另起一处 `matchMedia`，也不要用 antd 的 `Grid.useBreakpoint()`——后者首帧返回
 * 空对象，窄屏上会先闪一帧宽屏布局。
 *
 * `getSnapshot` 返回布尔而不是对象：`useSyncExternalStore` 用 `Object.is` 比较快照，
 * 每次新建对象会被判定为"一直在变"，直接死循环。
 */
export function useNarrowViewport(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, () => false)
}
