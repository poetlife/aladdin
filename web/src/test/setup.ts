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
