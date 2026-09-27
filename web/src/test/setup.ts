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
