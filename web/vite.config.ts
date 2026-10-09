import { existsSync } from 'node:fs'
import { join } from 'node:path'

import { defineConfig } from 'vitest/config'
import type { Plugin, ViteDevServer } from 'vite'
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

/** 一份送给 agent 的产物。形状与 src/pages/docs/chapters/artifacts.ts 里那个一致。 */
interface DocsArtifact {
  path: string
  contentType: string
  source: string
}

/**
 * 请求该由这里回答吗；该的话回答哪一份。
 *
 * 与 nginx 那两条规则**一一对应**（见 deploy/nginx-aladdin-site.conf）：
 * 静态资源类后缀找不到就 404，而不是落回 index.html；`Accept: text/markdown`
 * 时 `/docs/<章>` 直接给 Markdown。两处各写一份就会漂，而漂的表现是
 * "线上取到的是文档、本地取到的是首页"——只在一边复现。
 */
function artifactPathFor(path: string, accept: string | undefined): string | null {
  if (path.endsWith('.md') || path.endsWith('.txt')) {
    return path
  }
  // 只对**章**做这一条：放开到任意路径会让一个带该请求头的浏览器把整个应用取成 404。
  if (/^\/docs\/[a-z-]+$/.test(path) && accept?.includes('text/markdown') === true) {
    return `${path}.md`
  }
  return null
}

/**
 * dev 下提供送给 agent 的那几份产物（`/llms.txt`、`/docs/<章>.md`）。
 *
 * 生产里它们是**构建产物**（见 prerender/generate.tsx），由静态目录直接服务。
 * dev 没有那一趟构建，因此这里按同一份清单现场回答——用的是 Vite 自己的模块加载
 * （`ssrLoadModule`），因此读到的正是应用读的那一份源，不是另抄的一份。
 *
 * 顺带把"未知的 .md / .txt 不再兜底成 200 首页"这条也补上：那是个软 404，
 * 会让 agent 以为地址存在。
 */
function serveDocsArtifacts(): Plugin {
  return {
    name: 'serve-docs-artifacts',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const path = new URL(req.url ?? '/', 'http://localhost').pathname
        const target = artifactPathFor(path, req.headers.accept)
        if (target === null) {
          next()
          return
        }

        void (async () => {
          const artifact = (await loadArtifacts(server)).find((item) => item.path === target)
          if (artifact !== undefined) {
            res.setHeader('Content-Type', artifact.contentType)
            res.end(artifact.source)
            return
          }
          // public/ 下的真文件由 Vite 自己服务（robots.txt 就在那里）；其余一律 404。
          if (existsSync(join(server.config.publicDir, target))) {
            next()
            return
          }
          res.statusCode = 404
          res.end('Not Found')
        })().catch(next)
      })
    },
  }
}

/** 走 Vite 自己的模块加载取那份清单，因此 `?raw` 导入的章节源照常解析。 */
async function loadArtifacts(server: ViteDevServer): Promise<DocsArtifact[]> {
  const module = (await server.ssrLoadModule('/src/pages/docs/chapters/artifacts.ts')) as {
    docsArtifacts: () => DocsArtifact[]
  }
  return module.docsArtifacts()
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
  plugins: [react(), serveApiDocsIndex(), serveDocsArtifacts()],
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
