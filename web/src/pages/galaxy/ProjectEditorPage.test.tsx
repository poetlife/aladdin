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
  FileEntrySchema,
  GetCapabilitiesResponseSchema,
  GetDraftResponseSchema,
  GetProjectResponseSchema,
  ListAssetsResponseSchema,
  ListVersionsResponseSchema,
  PreviewDraftResponseSchema,
  ProjectSchema,
  SiteForm,
  ValidateDraftResponseSchema,
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
  saveVersion: vi.fn(),
  listVersions: vi.fn(),
  getVersion: vi.fn(),
  deleteVersion: vi.fn(),
  validateDraft: vi.fn(),
  previewDraft: vi.fn(),
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

/** 一份能力下发：默认配了桶（内容可用），按需覆盖。 */
function caps(overrides: { assetUploadEnabled?: boolean; publishEnabled?: boolean } = {}) {
  return create(GetCapabilitiesResponseSchema, {
    capabilities: create(CapabilitiesSchema, { assetUploadEnabled: true, ...overrides }),
  })
}

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
      project: create(ProjectSchema, { id: 'p1', name: '我的工程', form: SiteForm.STATIC }),
    }),
  )
  vi.mocked(galaxyApi.getDraft).mockResolvedValue(
    create(GetDraftResponseSchema, {
      draft: create(DraftSchema, {
        entries: [
          create(FileEntrySchema, {
            path: 'index.html',
            source: { case: 'digest', value: 'aa' },
            url: 'https://cos.example/signed/aa',
          }),
          create(FileEntrySchema, {
            path: 'style.css',
            source: { case: 'digest', value: 'bb' },
            url: 'https://cos.example/signed/bb',
          }),
        ],
      }),
    }),
  )
  vi.mocked(galaxyApi.listVersions).mockResolvedValue(create(ListVersionsResponseSchema, {}))
  vi.mocked(galaxyApi.previewDraft).mockResolvedValue(
    create(PreviewDraftResponseSchema, { html: '<h1>hi</h1>' }),
  )
  // 打开页面就会自动校验一次，因此每个用例都要有一个默认结论；
  // 不补的话 `vi.fn()` 返回 undefined，读 `response.problems` 直接抛。
  vi.mocked(galaxyApi.validateDraft).mockResolvedValue(create(ValidateDraftResponseSchema, {}))
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('工作台的形态', () => {
  it('默认落在预览：看得到渲染结果，源码不占版面', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
    )

    const container = await renderEditor()

    expect(container.querySelector('iframe')).not.toBeNull()
    // 预览渲染的是**服务端给的那份 HTML**。
    expect(container.querySelector('iframe')?.getAttribute('srcdoc')).toBe('<h1>hi</h1>')
    expect(container.querySelector('textarea')).toBeNull()
  })

  it('切到源码出现的是文件列表与那一份的原文，且**只读**', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
    )

    const container = await renderEditor()
    await switchStage(container, '源码')

    // 源码视图是"文件列表 + 选中的那一份"，不是"一份正文"。
    expect(container.textContent).toContain('index.html')
    expect(container.textContent).toContain('style.css')
    expect(container.querySelector('iframe')).toBeNull()
    // **网页端不改内容**：这块面积上没有任何可编辑的控件。
    expect(container.querySelector('textarea')).toBeNull()
  })

  it('预览侧给出"单独打开"，指向独立的那条路由', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
    )

    const container = await renderEditor()
    const link = Array.from(container.querySelectorAll('a')).find((candidate) =>
      candidate.textContent?.includes('单独打开'),
    )

    expect(link?.getAttribute('href')).toBe('/galaxy/p1/preview')
    expect(link?.getAttribute('target')).toBe('_blank')
  })

  // `static` 形态逐页预览，因此预览侧要能选看哪一页。
  it('static 形态的预览侧有页面选择器，且默认落在入口页', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
    )

    await renderEditor()

    expect(galaxyApi.previewDraft).toHaveBeenCalledWith('p1', 'index.html')
  })

  // 资产与版本是"一批东西"，与主区并排会让主区长期窄掉一截；改成从顶栏以弹层打开。
  it('资产与版本从顶栏以弹层打开，未打开时不占主区', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
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

describe('工作台的能力裁剪', () => {
  // spec 明确要求：未配置发布存储时不渲染发布入口，而不是渲染一个点了报错的控件。
  it('publish_enabled 为假时不渲染发布入口，即便持有发布权限', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps({ assetUploadEnabled: false, publishEnabled: false }),
    )

    const container = await renderEditor()

    expect(container.textContent).not.toContain('尚未发布')
    expect(findButton(container, '发布')).toBeUndefined()
  })

  // **桶是内容的前提**：没配置对象存储时，内容（草稿、版本与产物）整体不可用，
  // 因此不渲染内容相关的入口，改给一句说明——而不是渲染一个点了报错的控件。
  it('没配置对象存储时不渲染内容相关的入口，也不去问校验与预览', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps({ assetUploadEnabled: false }))

    const container = await renderEditor()

    expect(container.textContent).toContain('这个部署没有配置对象存储')
    expect(container.querySelector('iframe')).toBeNull()
    expect(findButtonExact(container, '版本')).toBeUndefined()
    expect(findButton(container, '存为版本')).toBeUndefined()
    // 没配桶时校验与预览必然失败，因此不去问。
    expect(galaxyApi.validateDraft).not.toHaveBeenCalled()
    expect(galaxyApi.previewDraft).not.toHaveBeenCalled()
  })

  it('publish_enabled 为真时才渲染发布入口', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps({ publishEnabled: true }),
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
      caps(),
    )

    const container = await renderEditor()

    // 校验的是**服务端的草稿**：请求里不带内容，写入只有命令行一条路。
    expect(galaxyApi.validateDraft).toHaveBeenCalledWith('p1')
    expect(container.textContent).toContain('可以发布')
  })

  it('把服务端返回的 problems 逐条列出（含文件与行号），而不是前端自己判断', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
    )
    vi.mocked(galaxyApi.validateDraft).mockResolvedValue(
      create(ValidateDraftResponseSchema, {
        problems: [
          create(ValidationProblemSchema, {
            message: '引用的资产不在本工程的资产库里',
            path: 'index.html',
            line: 3,
          }),
          create(ValidationProblemSchema, { message: '资源引用指向了本文件组之外' }),
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
    expect(document.body.textContent).toContain('这份草稿有以下问题，发布会被拒绝')
    expect(document.body.textContent).toContain('引用的资产不在本工程的资产库里')
    // 位置（哪一份文件、哪一行）由服务端给出。
    expect(document.body.textContent).toContain('index.html:3')
    expect(document.body.textContent).toContain('资源引用指向了本文件组之外')
  })

  // 「校验没跑成」与「内容有问题」是两件事：前者不是用户的工程坏了。
  it('校验请求失败呈现为"未完成"，不冒充内容有问题，预览照常', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
    )
    vi.mocked(galaxyApi.validateDraft).mockRejectedValue(new Error('网络断了'))

    const container = await renderEditor()

    expect(container.textContent).toContain('校验未完成')
    expect(container.textContent).not.toContain('有以下问题')
    expect(container.querySelector('iframe')).not.toBeNull()
  })

  // 渲染取不到内容（比如这个部署没配桶）时，只有预览那一块呈现失败——
  // 状态条、版本与资产照常可用，而不是整页变成一片失败。
  it('预览渲染失败只影响那一块，不把整页打成失败', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps({ publishEnabled: true }),
    )
    vi.mocked(galaxyApi.previewDraft).mockRejectedValue(new Error('本部署未配置对象存储'))

    const container = await renderEditor()

    expect(container.textContent).toContain('预览暂时渲染不出来')
    expect(container.textContent).toContain('本部署未配置对象存储')
    // 校验结论与流程状态照常呈现。
    expect(container.textContent).toContain('可以发布')
  })
})

describe('回到前台时重读草稿', () => {
  function setVisibility(state: DocumentVisibilityState): void {
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => state,
    })
  }

  it('重新可见时重拉草稿并重新校验；焦点与可见性接连到来只问一次', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())
    const container = await renderEditor()
    expect(container.textContent).toContain('可以发布')

    vi.mocked(galaxyApi.getDraft).mockResolvedValue(
      create(GetDraftResponseSchema, {
        draft: create(DraftSchema, {
          entries: [
            create(FileEntrySchema, {
              path: 'index.html',
              source: { case: 'digest', value: 'cc' },
              url: 'https://cos.example/signed/cc',
            }),
          ],
        }),
      }),
    )
    vi.mocked(galaxyApi.validateDraft).mockResolvedValue(
      create(ValidateDraftResponseSchema, {
        problems: [create(ValidationProblemSchema, { message: '缺了样式', path: 'index.html' })],
      }),
    )
    vi.mocked(galaxyApi.previewDraft).mockResolvedValue(
      create(PreviewDraftResponseSchema, { html: '<h1>新的</h1>' }),
    )

    const draftCalls = vi.mocked(galaxyApi.getDraft).mock.calls.length
    const validateCalls = vi.mocked(galaxyApi.validateDraft).mock.calls.length
    setVisibility('visible')
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
      window.dispatchEvent(new Event('focus'))
    })

    expect(vi.mocked(galaxyApi.getDraft).mock.calls.length).toBe(draftCalls + 1)
    expect(vi.mocked(galaxyApi.validateDraft).mock.calls.length).toBe(validateCalls + 1)
    expect(vi.mocked(galaxyApi.previewDraft).mock.calls.length).toBeGreaterThanOrEqual(2)
    expect(container.textContent).toContain('1 处问题')
    expect(container.textContent).not.toContain('可以发布')
    expect(container.querySelector('iframe')?.getAttribute('srcdoc')).toBe('<h1>新的</h1>')
  })

  it('页面隐藏时不重拉，避免把过期结论再问一遍', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())
    await renderEditor()
    const validateCalls = vi.mocked(galaxyApi.validateDraft).mock.calls.length

    setVisibility('hidden')
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
      window.dispatchEvent(new Event('focus'))
    })

    expect(vi.mocked(galaxyApi.validateDraft).mock.calls.length).toBe(validateCalls)
  })
})

describe('草稿有问题时的发布', () => {
  const sameEntries = [
    create(FileEntrySchema, {
      path: 'index.html',
      source: { case: 'digest', value: 'aa' },
    }),
    create(FileEntrySchema, {
      path: 'style.css',
      source: { case: 'digest', value: 'bb' },
    }),
  ]

  beforeEach(() => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps({ publishEnabled: true }))
    vi.mocked(galaxyApi.validateDraft).mockResolvedValue(
      create(ValidateDraftResponseSchema, {
        problems: [create(ValidationProblemSchema, { message: '引用坏了', path: 'index.html' })],
      }),
    )
  })

  it('与当前草稿清单相同的版本不可发布', async () => {
    vi.mocked(galaxyApi.listVersions).mockResolvedValue(
      create(ListVersionsResponseSchema, {
        versions: [create(VersionSchema, { id: 'v1', seq: 1n, entries: sameEntries })],
      }),
    )

    const container = await renderEditor()
    const publish = findButton(container, '发布')
    expect(publish, '没有找到发布按钮').not.toBeUndefined()
    expect(publish?.disabled).toBe(true)
  })

  it('清单不同的历史版本仍可发布', async () => {
    vi.mocked(galaxyApi.listVersions).mockResolvedValue(
      create(ListVersionsResponseSchema, {
        versions: [
          create(VersionSchema, { id: 'v1', seq: 1n, entries: sameEntries }),
          create(VersionSchema, {
            id: 'v0',
            seq: 0n,
            entries: [
              create(FileEntrySchema, {
                path: 'index.html',
                source: { case: 'digest', value: 'old' },
              }),
            ],
          }),
        ],
      }),
    )

    const container = await renderEditor()
    const publish = findButton(container, '发布')
    expect(publish?.disabled).toBe(false)

    await act(async () => {
      publish?.click()
    })
    const items = Array.from(document.body.querySelectorAll('[role="menuitem"]'))
    const blocked = items.find((item) => item.textContent?.includes('#1'))
    const openable = items.find((item) => item.textContent?.includes('#0'))
    expect(blocked, '没有找到与草稿相同的那一版').not.toBeUndefined()
    expect(blocked?.getAttribute('aria-disabled')).toBe('true')
    expect(openable?.getAttribute('aria-disabled')).not.toBe('true')
  })

  it('重拉失败时不再停留在上一次的可以发布', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())
    vi.mocked(galaxyApi.validateDraft).mockResolvedValue(create(ValidateDraftResponseSchema, {}))
    const container = await renderEditor()
    expect(container.textContent).toContain('可以发布')

    vi.mocked(galaxyApi.getDraft).mockRejectedValue(new Error('网络断了'))
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => 'visible',
    })
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })

    expect(container.textContent).toContain('校验未完成')
    expect(container.textContent).not.toContain('可以发布')
    expect(container.textContent).toContain('网络断了')
  })
})

// spec 的核查项：**接口面上不存在从网页端写内容的调用**。
//
// 这条断言看的是**真实的模块**（绕开本文件的 mock）：写入只有命令行一条路，
// 因此前端这一侧连可调用的入口都不该有。
describe('网页端不改内容', () => {
  it('前端 API 里没有写内容的入口', async () => {
    const real = await vi.importActual<Record<string, unknown>>('../../api/galaxy')
    for (const name of ['saveDraft', 'pushDraft', 'beginContentUpload', 'commitContentUpload']) {
      expect(real[name], `前端不该有 ${name} 这个写入口`).toBeUndefined()
    }
  })
})
