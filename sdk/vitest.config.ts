import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    // 三个面都在 node 里可测：插件与调用面本来就是 node 侧，运行期包是纯函数
    // 加上浏览器全局对象（测试自己注入），不需要真的起一个浏览器。
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
