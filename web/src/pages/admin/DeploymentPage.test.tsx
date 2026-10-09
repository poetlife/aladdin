import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../../api/identity'
import * as opsApi from '../../api/ops'
import { ConnectError, Code } from '../../api/errors'
import { SessionProvider } from '../../auth'
import { buildInfo } from '../../build/build-info'
import { ThemeProvider } from '../../theme'
import { DeploymentPage } from './DeploymentPage'

vi.mock('../../api/identity', () => ({
  AuthSource: { Google: 'google', Github: 'github' },
  getAuthMethods: vi.fn(),
  login: vi.fn(),
  whoAmI: vi.fn(),
  getSessionPermissions: vi.fn(),
}))

vi.mock('../../api/transport', () => ({
  onUnauthenticated: vi.fn(),
}))

vi.mock('../../api/ops', () => ({
  getDeploymentInfo: vi.fn(),
}))

// 前端那一侧的三项由构建期注入，测试进程里拿不到（不经 Makefile 就没有值）。
// 这里给一组固定值，好让"前后端提交号对照"这条分支真的被走到——否则本机构建下
// 它永远落在 unknown 那一档，而那一档恰恰是最不该出问题的地方。
vi.mock('../../build/build-info', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../build/build-info')>()
  return {
    ...actual,
    buildInfo: {
      version: 'v9.9.9-frontend',
      commit: 'frontendcommit',
      buildTime: '2026-10-09T10:00:00Z',
    },
  }
})

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

const PERMISSIONS = ['ops.deployment.read']

/** 后端那一侧的自述。字段与 proto 一一对应，测试按需要覆盖其中几项。 */
function deploymentInfo(overrides: Partial<Record<string, string>> = {}) {
  return {
    $typeName: 'aladdin.ops.v1.GetDeploymentInfoResponse' as const,
    version: 'v0.7.0',
    commit: 'frontendcommit',
    builtAt: '2026-10-08T01:02:03Z',
    released: true,
    goVersion: 'go1.27.1',
    os: 'linux',
    arch: 'amd64',
    instance: 'prod-1',
    startedAt: '2026-10-09T04:00:00Z',
    ...overrides,
  }
}

async function renderPage(): Promise<HTMLElement> {
  globalThis.localStorage.setItem('aladdin.token', 'tok')
  globalThis.localStorage.setItem('aladdin.scope', 'tenant/acme')
  vi.mocked(identityApi.whoAmI).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.WhoAmIResponse',
    subjectId: 'u1',
    subjectType: 'user',
    defaultScope: 'tenant/acme',
  })
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.GetSessionPermissionsResponse',
    scope: 'tenant/acme',
    permissions: PERMISSIONS,
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <SessionProvider>
          <DeploymentPage />
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  await act(async () => {})
  return container
}

/** 去掉空白再比：antd 会在两个汉字之间插一个空格。 */
function textOf(container: HTMLElement): string {
  return (container.textContent ?? '').replace(/\s+/g, '')
}

beforeEach(() => {
  vi.mocked(opsApi.getDeploymentInfo).mockResolvedValue(deploymentInfo())
})

afterEach(() => {
  act(() => root?.unmount())
  root = null
  document.body.innerHTML = ''
  globalThis.localStorage.clear()
  vi.clearAllMocks()
})

describe('DeploymentPage', () => {
  it('展示后端与前端两侧的版本、提交号与构建时间', async () => {
    const container = await renderPage()
    const text = textOf(container)

    expect(text).toContain('v0.7.0')
    expect(text).toContain('go1.27.1')
    expect(text).toContain('linux/amd64')
    expect(text).toContain('prod-1')
    expect(text).toContain('发布产物')
    // 前端那一侧取自构建期注入，不是从后端拿的。
    expect(text).toContain('v9.9.9-frontend')
  })

  it('两侧提交号一致时不报警', async () => {
    const container = await renderPage()
    expect(textOf(container)).not.toContain('提交号不一致')
  })

  it('两侧提交号不一致时给出醒目提示，并说清两种成因', async () => {
    vi.mocked(opsApi.getDeploymentInfo).mockResolvedValue(
      deploymentInfo({ commit: 'backendcommitdifferent' }),
    )

    const container = await renderPage()
    const text = textOf(container)

    expect(text).toContain('提交号不一致')
    // 缓存旧 bundle 与前后端分开发布是两种不同的处置方向，两种都要说。
    expect(text).toContain('缓存')
    expect(text).toContain('分开发布')
  })

  it('任一侧没有提交号时说「无从比较」，不说「不一致」', async () => {
    vi.mocked(opsApi.getDeploymentInfo).mockResolvedValue(deploymentInfo({ commit: '' }))

    const container = await renderPage()
    const text = textOf(container)

    expect(text).toContain('无从比较')
    expect(text).not.toContain('提交号不一致')
  })

  it('运行时长按启动时刻本地算，不额外请求接口', async () => {
    // 把启动时刻放在整整两小时前，uptime 就应当是「2 小时」——与"现在几点"无关的
    // 断言只能这么写，否则用例会随运行时刻漂。
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-09T06:00:00Z'))
    try {
      vi.mocked(opsApi.getDeploymentInfo).mockResolvedValue(
        deploymentInfo({ startedAt: '2026-10-09T04:00:00Z' }),
      )

      const container = await renderPage()
      expect(textOf(container)).toContain('2小时')
      // 读一次就够：uptime 不是靠轮询拿来的。
      expect(vi.mocked(opsApi.getDeploymentInfo)).toHaveBeenCalledTimes(1)

      // 走一分钟，展示跟着变，而接口调用数不变——这正是"本地刷新"的意思。
      await act(async () => {
        vi.advanceTimersByTime(60_000)
      })
      expect(textOf(container)).toContain('2小时1分')
      expect(vi.mocked(opsApi.getDeploymentInfo)).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('读不到时把失败原因与追踪 ID 都显示出来', async () => {
    // 带上 traceparent：服务端的失败响应同样回写链路标识，而排障时"下一步去搜什么"
    // 正是这一串。取值的实现见 api/trace-context。
    const traceparent = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'
    vi.mocked(opsApi.getDeploymentInfo).mockRejectedValue(
      new ConnectError('服务端不可用', Code.Unavailable, new Headers({ traceparent })),
    )

    const container = await renderPage()
    const text = textOf(container)

    expect(text).toContain('服务端不可用')
    expect(text).toContain('追踪ID')
    expect(text).toContain('4bf92f3577b34da6a3ce929d0e0e4736')
  })
})

// 前端那一侧的三项在真实构建里由 vite 的 define 注入，因此 buildInfo 的结构本身
// 也要钉一下：字段名与页面读的那几个必须对得上。
describe('buildInfo', () => {
  it('带着版本、提交号与构建时间三项', () => {
    expect(buildInfo.version).toBe('v9.9.9-frontend')
    expect(buildInfo.commit).toBe('frontendcommit')
    expect(buildInfo.buildTime).toBe('2026-10-09T10:00:00Z')
  })
})
