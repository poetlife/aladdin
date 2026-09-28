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
  CapabilitiesSchema,
  DraftSchema,
  GetCapabilitiesResponseSchema,
  GetDraftResponseSchema,
  GetProjectResponseSchema,
  ListVersionsResponseSchema,
  ProjectSchema,
  ValidateContentResponseSchema,
  ValidationProblemSchema,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { ProjectEditorPage } from './ProjectEditorPage'

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
  getCapabilities: vi.fn(),
  listProjects: vi.fn(),
  createProject: vi.fn(),
  getProject: vi.fn(),
  updateProject: vi.fn(),
  deleteProject: vi.fn(),
  getDraft: vi.fn(),
  saveDraft: vi.fn(),
  saveVersion: vi.fn(),
  listVersions: vi.fn(),
  getVersion: vi.fn(),
  deleteVersion: vi.fn(),
  validateContent: vi.fn(),
  listAssets: vi.fn(),
  beginAssetUpload: vi.fn(),
  commitAssetUpload: vi.fn(),
  deleteAsset: vi.fn(),
  publish: vi.fn(),
  unpublish: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

/** 渲染编辑器（路由里带一个 projectId）。 */
async function renderEditor(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <MemoryRouter initialEntries={['/galaxy/p1']}>
          <Routes>
            <Route path="/galaxy/:projectId" element={<ProjectEditorPage />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>,
    )
  })
  return container
}

/** 按文本找一颗按钮并点击。 */
async function clickButton(container: HTMLElement, label: string): Promise<void> {
  const button = Array.from(container.querySelectorAll('button')).find((candidate) =>
    candidate.textContent?.includes(label),
  )
  expect(button, `没有找到「${label}」按钮`).not.toBeUndefined()
  await act(async () => {
    button?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

beforeEach(() => {
  globalThis.localStorage?.clear()
  // 有令牌时 SessionProvider 才会去拉会话与权限码。
  globalThis.localStorage?.setItem('aladdin.token', 'test-token')

  vi.mocked(identityApi.whoAmI).mockResolvedValue(
    create(WhoAmIResponseSchema, { subjectId: 's1' }),
  )
  // 授予全部 galaxy 权限：用来证明"未渲染的入口是被部署能力裁掉的，
  // 而不是被权限裁掉的"。
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
    create(GetSessionPermissionsResponseSchema, {
      scope: '',
      permissions: [
        PermissionCodes.GalaxyProjectRead,
        PermissionCodes.GalaxyProjectWrite,
        PermissionCodes.GalaxyProjectPublish,
        PermissionCodes.GalaxyAssetRead,
        PermissionCodes.GalaxyAssetWrite,
      ],
    }),
  )

  vi.mocked(galaxyApi.getProject).mockResolvedValue(
    create(GetProjectResponseSchema, {
      project: create(ProjectSchema, { id: 'p1', name: '我的工程' }),
    }),
  )
  vi.mocked(galaxyApi.getDraft).mockResolvedValue(
    create(GetDraftResponseSchema, { draft: create(DraftSchema, { content: '<h1>hi</h1>' }) }),
  )
  vi.mocked(galaxyApi.listVersions).mockResolvedValue(create(ListVersionsResponseSchema, {}))
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('编辑器的能力裁剪', () => {
  // spec 明确要求：未配置发布存储时不渲染发布入口，而不是渲染一个点了报错的控件。
  it('publish_enabled 为假时不渲染发布入口，即便持有发布权限', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, {
        capabilities: create(CapabilitiesSchema, {
          assetUploadEnabled: false,
          publishEnabled: false,
        }),
      }),
    )

    const container = await renderEditor()

    expect(container.textContent).not.toContain('尚未发布')
    expect(container.textContent).not.toContain('发布这个版本')
    // 资产区同理：能力未启用时不渲染。
    expect(container.textContent).not.toContain('上传资产')
  })

  it('publish_enabled 为真时才渲染发布入口', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, {
        capabilities: create(CapabilitiesSchema, { publishEnabled: true }),
      }),
    )

    const container = await renderEditor()

    expect(container.textContent).toContain('尚未发布')
  })
})

describe('正文校验的呈现', () => {
  it('把服务端返回的 problems 逐条列出，而不是前端自己判断', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, { capabilities: create(CapabilitiesSchema, {}) }),
    )
    vi.mocked(galaxyApi.validateContent).mockResolvedValue(
      create(ValidateContentResponseSchema, {
        problems: [
          create(ValidationProblemSchema, { message: '第 3 行：引用指向不存在的资产「abc」' }),
          create(ValidationProblemSchema, { message: '第 9 行：引用指向外部地址' }),
        ],
      }),
    )

    const container = await renderEditor()
    await clickButton(container, '校验正文')

    expect(container.textContent).toContain('正文有以下问题，发布会被拒绝')
    expect(container.textContent).toContain('引用指向不存在的资产「abc」')
    expect(container.textContent).toContain('引用指向外部地址')
  })
})
