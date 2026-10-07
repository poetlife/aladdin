import { defineConfig } from 'vitest/config'
import type { Plugin } from 'vite'
import react from '@vitejs/plugin-react'

// dev 下让 /api-docs/ 落到目录里的 index.html。
//
// 生产由 nginx 的 try_files $uri $uri/ 处理，dev 不会——Vite 对目录请求会走
// SPA fallback，返回业务前端的 index.html（表现为打开文档页却看到登录页）。
// 补上这一步，开发和线上访问的是同一个地址。
function serveApiDocsIndex(): Plugin {
  return {
    name: 'serve-api-docs-index',
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        if (req.url === '/api-docs/') req.url = '/api-docs/index.html'
        next()
      })
    },
  }
}

export default defineConfig({
  plugins: [react(), serveApiDocsIndex()],
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
    // 前端自身路由是 /、/roles、/login、/login/callback、/forbidden，
    // 与 /aladdin. 不冲突。
    //
    // /auth/ 整段属于**服务端的浏览器直连端点**（重定向型登录渠道的起点与
    // 回调，见 internal/server/redirect_login_flow.go）。它同样必须转发到后端，
    // 否则本地点 GitHub 登录会被 SPA 兜底吃掉、表现为"点了没反应"。
    // 约定：前端不在 /auth/ 下放任何路由——它整段是服务端的。
    proxy: {
      '/aladdin.': {
        target: process.env.ALADDIN_API_TARGET ?? 'http://127.0.0.1:9090',
        changeOrigin: true,
      },
      '/auth/': {
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
