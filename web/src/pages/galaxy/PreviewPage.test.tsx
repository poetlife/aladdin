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
  GetProjectResponseSchema,
  PreviewDraftResponseSchema,
  ProjectSchema,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { PreviewPage } from './PreviewPage'

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
      project: create(ProjectSchema, { id: 'p1', name: '我的工程' }),
    }),
  )
  vi.mocked(galaxyApi.previewDraft).mockResolvedValue(
    create(PreviewDraftResponseSchema, { html: '<h1>hi</h1>' }),
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
  // 渲染在服务端、与发布共用同一段实现，因此这一页与工作台里的预览拿到的是
  // 同一份 HTML；沙箱属性也必须与内嵌时一致。
  it('渲染的是服务端给的草稿预览，且沙箱属性与内嵌时一致', async () => {
    const container = await renderPreviewPage()

    const sandbox = iframe(container).getAttribute('sandbox') ?? ''
    expect(sandbox).not.toContain('allow-same-origin')
    expect(sandbox).toContain('allow-scripts')
    expect(iframe(container).getAttribute('srcdoc')).toBe('<h1>hi</h1>')
    // 它读的是**草稿**，因此调用的是预览入口，而不是发布地址。
    expect(galaxyApi.previewDraft).toHaveBeenCalledWith('p1')
  })

  it('资产地址由服务端在渲染时补上，前端不再自己替换记号', async () => {
    vi.mocked(galaxyApi.previewDraft).mockResolvedValue(
      create(PreviewDraftResponseSchema, {
        html: '<img src="https://cos.example/signed/a1b2c3">',
      }),
    )

    const container = await renderPreviewPage()

    expect(iframe(container).getAttribute('srcdoc')).toContain('https://cos.example/signed/a1b2c3')
  })

  it('工程读不到时给出失败与重试，而不是一张空页', async () => {
    vi.mocked(galaxyApi.getProject).mockResolvedValue(create(GetProjectResponseSchema, {}))

    const container = await renderPreviewPage()

    expect(container.textContent).toContain('工程不存在或已被删除')
    expect(container.querySelector('iframe')).toBeNull()
  })
})
