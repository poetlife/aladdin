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
  ListAssetsResponseSchema,
  ListVersionsResponseSchema,
  ProjectSchema,
  ValidateContentResponseSchema,
  ValidationProblemSchema,
  VersionSchema,
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

/** 渲染工作台（路由里带一个 projectId）。 */
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

/** 按文本找一颗按钮；找不到返回 undefined。 */
function findButton(container: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(container.querySelectorAll('button')).find((candidate) =>
    candidate.textContent?.includes(label),
  )
}

/** 按**完全相等**的文本找按钮：`版本` 与 `存为版本` 只差两个字，包含匹配会挑错。 */
function findButtonExact(container: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(container.querySelectorAll('button')).find(
    (candidate) => candidate.textContent?.trim() === label,
  )
}

async function clickButton(button: HTMLButtonElement | undefined, label: string): Promise<void> {
  expect(button, `没有找到「${label}」按钮`).not.toBeUndefined()
  await act(async () => {
    button?.click()
  })
}

/** 点「预览 / 源码」切换里的某一档。触发的是它内部的那个 radio，与用户点击等价。 */
async function switchStage(container: HTMLElement, label: string): Promise<void> {
  const item = Array.from(container.querySelectorAll('.ant-segmented-item')).find((candidate) =>
    candidate.textContent?.includes(label),
  )
  expect(item, `没有找到「${label}」这一档`).not.toBeUndefined()
  await act(async () => {
    item?.querySelector('input')?.click()
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
  // 打开页面就会自动校验一次，因此每个用例都要有一个默认结论；
  // 不补的话 `vi.fn()` 返回 undefined，读 `response.problems` 直接抛。
  vi.mocked(galaxyApi.validateContent).mockResolvedValue(
    create(ValidateContentResponseSchema, {}),
  )
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('工作台的形态', () => {
  it('默认落在预览：看得到渲染结果，编辑区不占版面', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, { capabilities: create(CapabilitiesSchema, {}) }),
    )

    const container = await renderEditor()

    expect(container.querySelector('iframe')).not.toBeNull()
    expect(container.querySelector('textarea')).toBeNull()
  })

  it('切到源码才出现编辑区，与预览共用同一块面积', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, { capabilities: create(CapabilitiesSchema, {}) }),
    )

    const container = await renderEditor()
    await switchStage(container, '源码')

    expect(container.querySelector('textarea')).not.toBeNull()
    expect(container.querySelector('iframe')).toBeNull()
  })

  it('预览侧给出"单独打开"，指向独立的那条路由', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, { capabilities: create(CapabilitiesSchema, {}) }),
    )

    const container = await renderEditor()
    const link = Array.from(container.querySelectorAll('a')).find((candidate) =>
      candidate.textContent?.includes('单独打开'),
    )

    expect(link?.getAttribute('href')).toBe('/galaxy/p1/preview')
    expect(link?.getAttribute('target')).toBe('_blank')
  })

  // 资产与版本是"一批东西"，与主区并排会让主区长期窄掉一截；改成从顶栏以弹层打开。
  it('资产与版本从顶栏以弹层打开，未打开时不占主区', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, {
        capabilities: create(CapabilitiesSchema, { assetUploadEnabled: true }),
      }),
    )
    vi.mocked(galaxyApi.listAssets).mockResolvedValue(create(ListAssetsResponseSchema, {}))

    const container = await renderEditor()
    expect(container.textContent).not.toContain('资产库里还没有素材')

    await clickButton(findButtonExact(container, '版本'), '版本')
    expect(document.body.textContent).toContain('序号')

    await clickButton(findButtonExact(container, '资产'), '资产')
    expect(document.body.textContent).toContain('上传资产')
  })
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
    expect(findButton(container, '发布')).toBeUndefined()
    // 资产标签同理：能力未启用时不渲染。
    expect(container.textContent).not.toContain('资产')
  })

  it('publish_enabled 为真时才渲染发布入口', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, {
        capabilities: create(CapabilitiesSchema, { publishEnabled: true }),
      }),
    )
    vi.mocked(galaxyApi.listVersions).mockResolvedValue(
      create(ListVersionsResponseSchema, {
        versions: [create(VersionSchema, { id: 'v1', seq: 1n })],
      }),
    )

    const container = await renderEditor()

    expect(container.textContent).toContain('尚未发布')
    expect(findButton(container, '发布')).not.toBeUndefined()
  })
})

describe('校验结论自动产生', () => {
  it('打开页面就问服务端要结论，不需要先点任何按钮', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, { capabilities: create(CapabilitiesSchema, {}) }),
    )

    const container = await renderEditor()

    expect(galaxyApi.validateContent).toHaveBeenCalled()
    expect(container.textContent).toContain('可以发布')
  })

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

    // 状态条上先给出处数，逐条清单从那里展开——清单属于那份结论本身。
    expect(container.textContent).toContain('2 处问题')
    const trigger = Array.from(container.querySelectorAll('span')).find(
      (element) => element.textContent === '2 处问题',
    )
    expect(trigger, '没有找到可展开的结论').not.toBeUndefined()
    await act(async () => {
      trigger?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // 弹层挂在 document.body 上，因此断言看整份文档。
    expect(document.body.textContent).toContain('正文有以下问题，发布会被拒绝')
    expect(document.body.textContent).toContain('引用指向不存在的资产「abc」')
    expect(document.body.textContent).toContain('引用指向外部地址')
  })

  // 「校验没跑成」与「正文有问题」是两件事：前者不是用户的工程坏了。
  it('校验请求失败呈现为"未完成"，不冒充正文有问题，预览照常', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, { capabilities: create(CapabilitiesSchema, {}) }),
    )
    vi.mocked(galaxyApi.validateContent).mockRejectedValue(new Error('网络断了'))

    const container = await renderEditor()

    expect(container.textContent).toContain('校验未完成')
    expect(container.textContent).not.toContain('正文有以下问题')
    expect(container.querySelector('iframe')).not.toBeNull()
  })
})
