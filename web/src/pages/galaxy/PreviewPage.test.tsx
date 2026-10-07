import { create } from '@bufbuild/protobuf'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
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
  GetProjectResponseSchema,
  PreviewDraftResponseSchema,
  ProjectSchema,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { PreviewPage } from './PreviewPage'

// 服务端给的那条预览地址：它落在发布域上、带短时凭证，前端原样使用。
const previewURL = 'https://pub.example.com/g/p/tok/prj_x/index.html'

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

vi.mock('../../api/galaxy', () => ({
  getProject: vi.fn(),
  previewDraft: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPreviewPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <MemoryRouter initialEntries={['/galaxy/p1/preview']}>
          <Routes>
            <Route path="/galaxy/:projectId/preview" element={<PreviewPage />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>,
    )
  })
  return container
}

function iframe(container: HTMLElement): HTMLIFrameElement {
  const element = container.querySelector('iframe')
  expect(element, '没有渲染出 iframe').not.toBeNull()
  return element as HTMLIFrameElement
}

beforeEach(() => {
  globalThis.localStorage?.clear()
  globalThis.localStorage?.setItem('aladdin.token', 'test-token')

  vi.mocked(identityApi.whoAmI).mockResolvedValue(
    create(WhoAmIResponseSchema, { subjectId: 's1' }),
  )
  // 只给读权限：看一眼草稿不该要求能改它。
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
    create(GetSessionPermissionsResponseSchema, {
      scope: '',
      permissions: [PermissionCodes.GalaxyProjectRead],
    }),
  )

  vi.mocked(galaxyApi.getProject).mockResolvedValue(
    create(GetProjectResponseSchema, {
      project: create(ProjectSchema, {
        id: 'p1',
        name: '我的工程',
        slots: [{ slot: ContentSlot.SITE }],
      }),
    }),
  )
  vi.mocked(galaxyApi.previewDraft).mockResolvedValue(
    create(PreviewDraftResponseSchema, { url: previewURL }),
  )
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('单独打开的预览页', () => {
  // 这一页与工作台里的预览是同一条通道：服务端给一条带短时凭证的地址，前端原样
  // 放进 iframe；沙箱属性也必须与内嵌时一致。
  it('加载的是服务端给的草稿地址，且沙箱属性与内嵌时一致', async () => {
    const container = await renderPreviewPage()

    const sandbox = iframe(container).getAttribute('sandbox') ?? ''
    expect(sandbox).not.toContain('allow-same-origin')
    expect(sandbox).toContain('allow-scripts')
    expect(iframe(container).getAttribute('src')).toBe(previewURL)
    // 它读的是**草稿**，因此调用的是预览入口，而不是发布地址。
    expect(galaxyApi.previewDraft).toHaveBeenCalledWith('p1', ContentSlot.SITE)
  })

  it('地址原样使用：前端不拼路径，也不往里塞字节', async () => {
    vi.mocked(galaxyApi.previewDraft).mockResolvedValue(
      create(PreviewDraftResponseSchema, { url: 'https://pub.example.com/g/p/tok2/prj_x/style.css' }),
    )

    const container = await renderPreviewPage()

    const element = iframe(container)
    expect(element.getAttribute('src')).toBe('https://pub.example.com/g/p/tok2/prj_x/style.css')
    expect(element.getAttribute('srcdoc')).toBeNull()
  })

  // 草稿里还没有入口时服务端给空地址：那是**空态**，不是一个打不开的 iframe。
  it('草稿为空时给空态，而不是加载一个空地址', async () => {
    vi.mocked(galaxyApi.previewDraft).mockResolvedValue(create(PreviewDraftResponseSchema, {}))

    const container = await renderPreviewPage()

    expect(container.textContent).toContain('草稿还是空的')
    expect(container.querySelector('iframe')).toBeNull()
  })

  it('工程读不到时给出失败与重试，而不是一张空页', async () => {
    vi.mocked(galaxyApi.getProject).mockResolvedValue(create(GetProjectResponseSchema, {}))

    const container = await renderPreviewPage()

    expect(container.textContent).toContain('工程不存在或已被删除')
    expect(container.querySelector('iframe')).toBeNull()
  })
})
