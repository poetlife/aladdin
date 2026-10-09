import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../../api/identity'
import * as telemetryAdminApi from '../../api/telemetry-admin'
import { Code, ConnectError } from '../../api/errors'
import { SessionProvider } from '../../auth'
import { ThemeProvider } from '../../theme'
import { TimeWindow } from '../../gen/proto/aladdin/telemetry/v1/telemetry_admin_pb'
import { Client, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { TelemetryPage } from './TelemetryPage'

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

vi.mock('../../api/telemetry-admin', () => ({
  listEventStats: vi.fn(),
  listRecentEvents: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

const PERMISSIONS = ['telemetry.read']

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
          <TelemetryPage />
        </SessionProvider>
      </ThemeProvider>,
    )
  })
  await act(async () => {})
  return container
}

/** 去掉空白再比：antd 会在两个汉字之间插一个空格。 */
function segmentByText(container: HTMLElement, text: string): HTMLElement | undefined {
  return [...container.querySelectorAll<HTMLElement>('.ant-segmented-item')].find((item) =>
    (item.textContent ?? '').replace(/\s+/g, '').includes(text),
  )
}

function statsResponse(count: bigint) {
  return {
    $typeName: 'aladdin.telemetry.v1.ListEventStatsResponse' as const,
    stats: [
      {
        $typeName: 'aladdin.telemetry.v1.EventStat' as const,
        client: Client.WEB,
        action: 'draft.save',
        result: Result.BLOCKED,
        count,
      },
    ],
  }
}

function eventsResponse() {
  return {
    $typeName: 'aladdin.telemetry.v1.ListRecentEventsResponse' as const,
    events: [
      {
        $typeName: 'aladdin.telemetry.v1.RecentEvent' as const,
        occurredAt: '2026-09-30T04:30:00Z',
        client: Client.CLI,
        surface: Surface.CLI,
        action: 'cli.local_fail',
        result: Result.FAIL,
        durationMs: 0,
        clientTraceId: '',
        subjectId: '',
        attrs: { command: 'galaxy' },
      },
    ],
    subjects: {},
  }
}

/** 一条带主体的明细：展示名与头像由服务端解析好随响应给出。 */
function resolvedEventsResponse() {
  return {
    $typeName: 'aladdin.telemetry.v1.ListRecentEventsResponse' as const,
    events: [
      {
        $typeName: 'aladdin.telemetry.v1.RecentEvent' as const,
        occurredAt: '2026-09-30T04:30:00Z',
        client: Client.WEB,
        surface: Surface.WEB_EDITOR,
        action: 'editor.open',
        result: Result.OK,
        durationMs: 128,
        clientTraceId: '',
        subjectId: 'usr_UHecUHysZH17mlYkEV2CYA',
        attrs: {},
      },
      {
        // 另一个人：没设昵称、渠道也没有可读标识，**展示名就是标识本身**——
        // 这一列不该把同一个字符串显示两遍。
        $typeName: 'aladdin.telemetry.v1.RecentEvent' as const,
        occurredAt: '2026-09-30T04:29:00Z',
        client: Client.WEB,
        surface: Surface.WEB_PREVIEW,
        action: 'preview.toggle',
        result: Result.OK,
        durationMs: 0,
        clientTraceId: '',
        subjectId: 'usr_noName',
        attrs: {},
      },
    ],
    subjects: {
      usr_UHecUHysZH17mlYkEV2CYA: {
        $typeName: 'aladdin.telemetry.v1.SubjectProfile' as const,
        displayName: '阿拉丁',
        avatarUrl: '',
      },
      // 这个人没设昵称、渠道也没有可读标识：展示名就是标识本身。
      usr_noName: {
        $typeName: 'aladdin.telemetry.v1.SubjectProfile' as const,
        displayName: 'usr_noName',
        avatarUrl: '',
      },
    },
  }
}

beforeEach(() => {
  globalThis.localStorage?.clear()
  vi.mocked(telemetryAdminApi.listEventStats).mockResolvedValue(statsResponse(3n))
  vi.mocked(telemetryAdminApi.listRecentEvents).mockResolvedValue(eventsResponse())
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
  vi.clearAllMocks()
})

describe('客户端遥测页', () => {
  // 计数与明细必须用同一个窗口取数：各用各的口径会出现"计数说没有失败、
  // 明细里却列着一条失败"这种自相矛盾。
  it('默认以近 7 天取计数与明细', async () => {
    const container = await renderPage()

    expect(telemetryAdminApi.listEventStats).toHaveBeenCalledWith('tenant/acme', TimeWindow.LAST_7_DAYS)
    expect(telemetryAdminApi.listRecentEvents).toHaveBeenCalledWith(
      'tenant/acme',
      TimeWindow.LAST_7_DAYS,
    )
    // 计数：动作名 + 被拦下。
    expect(container.textContent).toContain('draft.save')
    expect(container.textContent).toContain('被拦下')
    // 明细：命令行本地失败 + 匿名 + 白名单属性。
    expect(container.textContent).toContain('cli.local_fail')
    expect(container.textContent).toContain('匿名')
    expect(container.textContent).toContain('command=galaxy')
  })

  it('切换时间窗后按新窗口重新取数', async () => {
    const container = await renderPage()

    const today = segmentByText(container, '今日')
    expect(today, '没有找到「今日」这个时间窗').not.toBeUndefined()
    await act(async () => {
      today?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(telemetryAdminApi.listEventStats).toHaveBeenLastCalledWith(
      'tenant/acme',
      TimeWindow.TODAY,
    )
    expect(telemetryAdminApi.listRecentEvents).toHaveBeenLastCalledWith(
      'tenant/acme',
      TimeWindow.TODAY,
    )
  })

  // 读取失败不该被说成"没有数据"：把服务端的理由与可对齐日志的追踪 ID 一起给出。
  it('查询失败时呈现服务端理由与追踪 ID', async () => {
    vi.mocked(telemetryAdminApi.listEventStats).mockRejectedValue(
      new ConnectError(
        '遥测存储暂时不可用',
        Code.Unavailable,
        new Headers({ 'x-trace-id': '4bf92f3577b34da6a3ce929d0e0e4736' }),
      ),
    )

    const container = await renderPage()

    expect(container.textContent).toContain('遥测存储暂时不可用')
    expect(container.textContent).toContain('4bf92f3577b34da6a3ce929d0e0e4736')
  })

  // 主体列显示的是**服务端解析好的**展示名与头像，标识仍以小字可见可复制。
  //
  // 页面不自己拼名字：它只渲染服务端给的值，回退规则只有服务端那一处实现。
  it('主体列显示头像与展示名，标识仍可复制', async () => {
    vi.mocked(telemetryAdminApi.listRecentEvents).mockResolvedValue(resolvedEventsResponse())

    const container = await renderPage()

    expect(container.textContent).toContain('阿拉丁')
    expect(container.textContent).toContain('usr_UHecUHysZH17mlYkEV2CYA')
    // 没有头像时用展示名的首字符占位（与侧边栏、档案页同一份实现）。
    const avatar = container.querySelector('.ant-avatar')
    expect(avatar?.textContent).toContain('阿')
    // 复制入口在标识那一行上，而不是在展示名上——排查时要抄走的是标识。
    expect(container.querySelectorAll('.ant-typography-copy').length).toBeGreaterThan(0)
  })

  // 展示名就是标识时只渲染标识：把同一个字符串显示两遍是纯噪声。
  it('展示名与标识相同时只显示标识', async () => {
    vi.mocked(telemetryAdminApi.listRecentEvents).mockResolvedValue(resolvedEventsResponse())

    const container = await renderPage()

    const text = container.textContent ?? ''
    expect(text.split('usr_noName').length - 1).toBe(1)
  })

  // 服务端没解析出展示信息时（档案存储抖动、或没装配档案面）回退到只显示标识，
  // 而不是让这一列变成空白。
  it('拿不到展示信息时回退到只显示标识', async () => {
    vi.mocked(telemetryAdminApi.listRecentEvents).mockResolvedValue({
      ...resolvedEventsResponse(),
      subjects: {},
    })

    const container = await renderPage()

    expect(container.textContent).toContain('usr_UHecUHysZH17mlYkEV2CYA')
    expect(container.textContent).not.toContain('阿拉丁')
    expect(container.querySelector('.ant-avatar')).toBeNull()
  })

  // 耗时列：有值显示毫秒数，0 显示「—」，且列头解释为什么会有空的。
  it('耗时列区分有值与无起止', async () => {
    vi.mocked(telemetryAdminApi.listRecentEvents).mockResolvedValue(resolvedEventsResponse())

    const container = await renderPage()

    expect(container.textContent).toContain('128 ms')
    // 同一页里那条没有起止的动作（preview.toggle 切成源码那种形状）显示「—」。
    expect(container.textContent).toContain('—')
    expect(container.textContent).toContain('耗时')
  })
})
