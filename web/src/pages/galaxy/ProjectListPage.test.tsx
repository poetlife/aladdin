import { create } from '@bufbuild/protobuf'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as galaxyApi from '../../api/galaxy'
import * as identityApi from '../../api/identity'
import { SessionProvider } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import {
  GetSessionPermissionsResponseSchema,
  WhoAmIResponseSchema,
} from '../../gen/proto/aladdin/identity/v1/identity_pb'
import {
  ContentSlot,
  DeleteProjectResponseSchema,
  ListProjectsResponseSchema,
  ProjectSchema,
  UnpublishResponseSchema,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { track } from '../../telemetry/track'
import { ProjectListPage } from './ProjectListPage'

// 遥测走真实模块的话它会去调还没初始化的传输层（本文件没有初始化它），
// 于是每次上报都只落一条 console.debug。这里换成替身，好让"报了没有、带了什么"
// 变成可断言的事实。
vi.mock('../../telemetry/track', () => ({ track: vi.fn() }))

vi.mock('../../api/identity', () => ({
  AuthSource: { Google: 'google', Github: 'github' },
  getAuthMethods: vi.fn(),
  login: vi.fn(),
  loginWithGoogle: vi.fn(),
  whoAmI: vi.fn(),
  getSessionPermissions: vi.fn(),
}))

vi.mock('../../api/transport', () => ({
  onUnauthenticated: vi.fn(),
}))

vi.mock('../../api/galaxy', () => ({
  listProjects: vi.fn(),
  createProject: vi.fn(),
  deleteProject: vi.fn(),
  unpublish: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderList(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <MemoryRouter>
          <ProjectListPage />
        </MemoryRouter>
      </SessionProvider>,
    )
  })
  return container
}

beforeEach(() => {
  // 每个用例从零开始数调用次数：本文件有多个"确认之后调了几次"的断言。
  vi.clearAllMocks()
  globalThis.localStorage?.clear()
  globalThis.localStorage?.setItem('aladdin.token', 'test-token')
  vi.mocked(identityApi.whoAmI).mockResolvedValue(create(WhoAmIResponseSchema, { subjectId: 's1' }))
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
    create(GetSessionPermissionsResponseSchema, {
      scope: '',
      permissions: [PermissionCodes.GalaxyProjectRead, PermissionCodes.GalaxyProjectWrite],
    }),
  )
  vi.mocked(galaxyApi.listProjects).mockResolvedValue(
    create(ListProjectsResponseSchema, {
      projects: [
        create(ProjectSchema, {
          id: 'p1',
          name: '我的工程',
          slots: [{ slot: ContentSlot.SITE }],
        }),
      ],
    }),
  )
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('删除工程', () => {
  it('确认后按钮进入 loading，并挡住第二次提交', async () => {
    let resolveDelete: (() => void) | undefined
    vi.mocked(galaxyApi.deleteProject).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveDelete = () => resolve(create(DeleteProjectResponseSchema, {}))
        }),
    )

    const container = await renderList()
    const trigger = Array.from(container.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('删除'),
    )
    expect(trigger, '没有找到删除按钮').not.toBeUndefined()

    await act(async () => {
      trigger?.click()
    })

    // antd 会在按钮文字的汉字之间插入空格，所以确认按钮的文本是「删 除」。
    const ok = Array.from(document.body.querySelectorAll('button')).find(
      (button) => button !== trigger && button.textContent?.replace(/\s/g, '') === '删除',
    )
    expect(ok, '没有找到确认按钮').not.toBeUndefined()

    await act(async () => {
      ok?.click()
      ok?.click()
    })

    expect(galaxyApi.deleteProject).toHaveBeenCalledTimes(1)
    expect(galaxyApi.deleteProject).toHaveBeenCalledWith('p1')
    expect(ok?.className).toContain('ant-btn-loading')
    expect(trigger?.disabled).toBe(true)

    await act(async () => {
      resolveDelete?.()
    })
    expect(vi.mocked(galaxyApi.listProjects).mock.calls.length).toBeGreaterThan(1)
  })
})

/** 让当前会话持有发布权限。撤回按 `galaxy.project.publish` 裁剪。 */
function grantPublishPermission(): void {
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
    create(GetSessionPermissionsResponseSchema, {
      scope: '',
      permissions: [PermissionCodes.GalaxyProjectRead, PermissionCodes.GalaxyProjectPublish],
    }),
  )
}

/** 一个已发布的站点槽：列表里因此出现地址与撤回。 */
function listOnePublishedProject(): void {
  vi.mocked(galaxyApi.listProjects).mockResolvedValue(
    create(ListProjectsResponseSchema, {
      projects: [
        create(ProjectSchema, {
          id: 'p1',
          name: '我的工程',
          slots: [
            {
              slot: ContentSlot.SITE,
              published: true,
              publishedUrl: 'https://app.example.com/g/p1',
            },
          ],
        }),
      ],
    }),
  )
}

/** 在整页里找一个按钮：antd 会在汉字之间插空格，因此按去空格后的文本比。 */
function findButton(scope: ParentNode, label: string): HTMLButtonElement | undefined {
  return Array.from(scope.querySelectorAll('button')).find(
    (button) => button.textContent?.replace(/\s/g, '') === label,
  )
}

describe('列表里撤回发布', () => {
  // 这块入口存在的理由：**能看到地址的地方就该能在那里把它作废**。以前列表只显示
  // "已发布 + 地址"，撤回要先打开工作台、再去找状态条。
  it('确认后按槽撤回，并刷新列表', async () => {
    grantPublishPermission()
    listOnePublishedProject()
    vi.mocked(galaxyApi.unpublish).mockResolvedValue(create(UnpublishResponseSchema, {}))

    const container = await renderList()
    expect(container.textContent, '已发布的地址没有显示出来').toContain(
      'https://app.example.com/g/p1',
    )
    const trigger = findButton(container, '撤回发布')
    expect(trigger, '地址旁边没有撤回入口').not.toBeUndefined()

    await act(async () => {
      trigger?.click()
    })
    const ok = findButton(document.body, '撤回')
    expect(ok, '没有找到确认按钮').not.toBeUndefined()

    await act(async () => {
      ok?.click()
    })

    expect(galaxyApi.unpublish).toHaveBeenCalledTimes(1)
    expect(galaxyApi.unpublish).toHaveBeenCalledWith('p1', ContentSlot.SITE)
    expect(vi.mocked(galaxyApi.listProjects).mock.calls.length).toBeGreaterThan(1)
  })

  // 取消是"动作没发生"：请求不发出去。与工作台那一处同一条。
  it('取消确认不发请求', async () => {
    grantPublishPermission()
    listOnePublishedProject()

    const container = await renderList()
    const trigger = findButton(container, '撤回发布')
    await act(async () => {
      trigger?.click()
    })
    // 取消按钮按"不是主按钮"取：这个用例没挂 ConfigProvider，语言包是默认那一份，
    // 按文案找会随环境变。
    const ok = findButton(document.body, '撤回')
    const cancel = ok?.parentElement?.querySelector<HTMLButtonElement>('button:not(.ant-btn-primary)')
    expect(cancel, '没有找到取消按钮').not.toBeUndefined()

    await act(async () => {
      cancel?.click()
    })

    expect(galaxyApi.unpublish).not.toHaveBeenCalled()
  })

  // 不持有发布权限时**不渲染入口**，而不是渲染一个点了报错的控件——地址照常显示。
  it('没有发布权限时不渲染撤回', async () => {
    listOnePublishedProject()

    const container = await renderList()

    expect(container.textContent).toContain('https://app.example.com/g/p1')
    expect(findButton(container, '撤回发布')).toBeUndefined()
  })
})

describe('打开列表的遥测', () => {
  const traceID = '4bf92f3577b34da6a3ce929d0e0e4736'

  // 服务端的 trace_id 只在响应头里，而成功时没有错误对象可读——值因此只能由
  // 调用方交给调用的那个捕获点带回来。这条断言守的就是那根线：它一旦断了，
  // 管理页上那一列会静默地全空，看起来像"服务端没回写"。
  it('成功打开时带上这次 listProjects 的 trace_id', async () => {
    vi.mocked(galaxyApi.listProjects).mockImplementation(async (trace) => {
      trace?.onHeader(new Headers({ 'x-trace-id': traceID }))
      return create(ListProjectsResponseSchema, { projects: [] })
    })

    await renderList()

    expect(track).toHaveBeenCalledWith(
      expect.objectContaining({
        action: Action.PROJECT_LIST_OPEN,
        result: Result.OK,
        traceId: traceID,
      }),
    )
  })

  // 请求没走到服务端、或服务端没回写时，这个字段必须是空的：
  // 编一个查不到的 ID 出来会把排障引向"日志丢了"。
  it('响应没带链路标识时该字段为空，而不是编一个', async () => {
    vi.mocked(galaxyApi.listProjects).mockImplementation(async () =>
      create(ListProjectsResponseSchema, { projects: [] }),
    )

    await renderList()

    expect(track).toHaveBeenCalledWith(
      expect.objectContaining({ action: Action.PROJECT_LIST_OPEN, traceId: undefined }),
    )
  })
})
