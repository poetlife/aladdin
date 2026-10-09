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

/**
 * 构建期注入的版本三项。
 *
 * 取值由 Makefile 的 WEB_BUILD_ENV 传进来，与后端 LDFLAGS 取自**同一处**
 * （同一个 VERSION / COMMIT / BUILD_TIME）——这正是部署信息页能回答"前后端是不是
 * 同一次构建"的前提：两侧各算一份，那道比较就只是在比两个不相干的字符串
 * （见 docs/design/deployment/README.md）。
 *
 * 缺省值是 dev：不经 Makefile 的构建（IDE 里直接跑 vite、CI 里的裸 vite build）
 * 得到的就是它，与后端的缺省值一样诚实——不编一个看起来像真的版本号。
 */
const buildVersion = process.env.ALADDIN_BUILD_VERSION ?? 'dev'
const buildCommit = process.env.ALADDIN_BUILD_COMMIT ?? ''
const buildTime = process.env.ALADDIN_BUILD_TIME ?? ''

export default defineConfig({
  plugins: [react(), serveApiDocsIndex()],
  // 值的读取只有一处实现：web/src/build/build-info.ts 在这里声明的三个全局上。
  // define 是**文本替换**，因此名字必须与那边声明的完全一致——写错不会报错，
  // 只会在运行时抛 ReferenceError。
  define: {
    __BUILD_VERSION__: JSON.stringify(buildVersion),
    __BUILD_COMMIT__: JSON.stringify(buildCommit),
    __BUILD_TIME__: JSON.stringify(buildTime),
  },
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
