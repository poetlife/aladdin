import { NARROW_MEDIA_QUERY } from '../layouts/use-narrow-viewport'

type ChangeListener = (event: MediaQueryListEvent) => void

// 只有窄屏查询命中，其余一律 false——与 test/setup.ts 里那个补丁的默认行为一致，
// 差别只在于这里的值可以被测试改。
let narrow = false
const listeners = new Set<ChangeListener>()

/**
 * 装上一个可被测试驱动的 `matchMedia`。
 *
 * `test/setup.ts` 里那个补丁恒返回 `matches: false`、且监听器是空函数，
 * 于是"窄屏会怎样"根本测不出来。这个替身把当前宽窄记在模块里：
 * 初值由 `initialNarrow` 给，之后的切换走 `setNarrow`。
 *
 * 查询串取自 `useNarrowViewport` 自己导出的常量，不另抄一份——
 * 抄一份就会出现"测试改了断点、代码没改"这种两处不一致。
 */
export function installMatchMedia(initialNarrow: boolean): void {
  narrow = initialNarrow
  listeners.clear()
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: query === NARROW_MEDIA_QUERY ? narrow : false,
      media: query,
      onchange: null,
      addEventListener: (_type: string, listener: ChangeListener) => {
        listeners.add(listener)
      },
      removeEventListener: (_type: string, listener: ChangeListener) => {
        listeners.delete(listener)
      },
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList
}

/** 切换当前宽窄，并触发 `change`，让订阅方（如 useNarrowViewport）跟着重渲染。 */
export function setNarrow(value: boolean): void {
  narrow = value
  const event = { matches: value } as MediaQueryListEvent
  listeners.forEach((listener) => {
    listener(event)
  })
}
