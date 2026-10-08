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
})
