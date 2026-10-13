import { afterEach } from 'vitest'

// 测试环境的统一准备。
//
// jsdom 未实现 matchMedia，而 antd 的响应式组件依赖它；
// 不打这个补丁，所有渲染 antd 组件的测试都会在挂载时抛错。
if (!globalThis.matchMedia) {
  globalThis.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}

// jsdom 同样没有 ResizeObserver，而 antd 的气泡类组件（Popconfirm / Popover /
// Tooltip）用它测量尺寸；不打这个补丁，任何打开气泡的测试都会在挂载时抛错。
if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  } as unknown as typeof ResizeObserver
}

const nativeSetTimeout = globalThis.setTimeout.bind(globalThis)
const nativeClearTimeout = globalThis.clearTimeout.bind(globalThis)
const pendingTimeouts = new Set<ReturnType<typeof setTimeout>>()

// antd/rc 组件内部有若干延时状态（例如 Form 的 debounce）。测试结束时若还没触发，
// 回调可能拖到 jsdom 销毁后才 setState，React 读取 window 时就会抛未处理异常。
globalThis.setTimeout = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
  const timeoutId = nativeSetTimeout(() => {
    pendingTimeouts.delete(timeoutId)
    if (typeof handler === 'function') {
      handler(...args)
    }
  }, timeout)
  pendingTimeouts.add(timeoutId)
  return timeoutId
}) as typeof setTimeout

globalThis.clearTimeout = ((timeoutId?: ReturnType<typeof setTimeout>) => {
  if (timeoutId !== undefined) {
    pendingTimeouts.delete(timeoutId)
  }
  nativeClearTimeout(timeoutId)
}) as typeof clearTimeout

afterEach(() => {
  for (const timeoutId of pendingTimeouts) {
    nativeClearTimeout(timeoutId)
  }
  pendingTimeouts.clear()
})
