import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    // 开发期把 RPC 请求转发到后端。
    //
    // Connect handler 挂在过程名本身（/aladdin.<领域>.<版本>.<Service>/<Method>），
    // 没有 /api 之类的统一前缀——这样服务端不需要剥前缀就能解析出过程名，
    // 少一处两侧必须对齐的魔法字符串。代价是代理规则按包名前缀转发。
    //
    // 用字符串前缀而不是正则：vite 对字符串键做前缀匹配，语义比正则直白，
    // 也不会因为转义层级出问题。
    // 前端自身路由是 /、/roles、/login、/forbidden，与 /aladdin. 不冲突。
    proxy: {
      '/aladdin.': {
        target: process.env.ALADDIN_API_TARGET ?? 'http://127.0.0.1:9090',
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
  },
})
