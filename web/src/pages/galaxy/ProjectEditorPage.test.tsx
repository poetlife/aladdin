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
  type Asset,
  AssetSchema,
  CapabilitiesSchema,
  DraftSchema,
  FileEntrySchema,
  GetCapabilitiesResponseSchema,
  GetDraftResponseSchema,
  GetProjectResponseSchema,
  ListAssetsResponseSchema,
  ListVersionsResponseSchema,
  PreviewDraftResponseSchema,
  ContentSlot,
  ProjectSchema,
  UpdateAssetResponseSchema,
  ValidateDraftResponseSchema,
  ValidationProblemSchema,
  VersionSchema,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import * as eventsApi from '../../api/events'
import { installMatchMedia } from '../../test/match-media'
import { fakeTopicStream, type FakeTopicStream } from '../../test/topic-event-stream'
import { projectTopic } from '../../watch/topics'
import { ProjectEditorPage } from './ProjectEditorPage'

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

vi.mock('../../api/events', () => ({
  watchTopics: vi.fn(),
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
  updateAsset: vi.fn(),
  deleteAsset: vi.fn(),
  publish: vi.fn(),
  unpublish: vi.fn(),
}))

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

// 服务端给的那条预览地址：落在发布域上、带短时凭证，前端原样放进 iframe。
const previewURL = 'https://pub.example.com/g/p/tok/prj_x/index.html'

/** 一份能力下发：默认配了桶（内容可用）与发布域（预览可用），按需覆盖。 */
function caps(
  overrides: { assetUploadEnabled?: boolean; publishEnabled?: boolean; previewEnabled?: boolean } = {},
) {
  const assetUploadEnabled = overrides.assetUploadEnabled ?? true
  return create(GetCapabilitiesResponseSchema, {
    capabilities: create(CapabilitiesSchema, {
      assetUploadEnabled,
      // 预览还要求发布域；没有桶就没有内容，也就没有预览（与服务端的判据一致）。
      previewEnabled: assetUploadEnabled,
      ...overrides,
    }),
  })
}

/**
 * 一条**安静的流**：不产出任何事件，直到被中止。它表示"这段时间没有变化"。
 *
 * 它是订阅的默认行为：工作台挂载即订阅，而裸 `vi.fn()` 返回 undefined，
 * `for await` 会直接抛——那被兜底逻辑咽掉之后还会安排一次重连，于是每个用例
 * 都多出一个定时器。
 */
function quietStream(signal: AbortSignal) {
  return fakeTopicStream(signal).stream
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

/**
 * 让首屏那一串顺序 await 走完。
 *
 * 加载是多步顺序请求（工程 + 能力，然后草稿 + 版本，再资产、校验、预览），一次
 * act 冲刷不保证走到底。而"事件到达之后重拉了几次"这类断言必须先有一个稳定的
 * 基线——否则量到的基线本身还在动。
 */
async function settle(): Promise<void> {
  for (let round = 0; round < 5; round += 1) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
  }
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

/** 按 `aria-label` 找一颗按钮：窄屏下不少控件只剩图标，文本定位不到它们。 */
function findButtonByLabel(container: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(container.querySelectorAll('button')).find(
    (candidate) => candidate.getAttribute('aria-label') === label,
  )
}

/** 打开顶栏的「更多」（窄屏把集合入口收在这里）。 */
async function openMore(container: HTMLElement): Promise<void> {
  await clickButton(findButtonByLabel(container, '更多'), '更多')
}

/**
 * 菜单里的一项。antd 把菜单挂在 `document.body` 上，因此看整份文档——
 * 与既有的发布菜单用例同一做法（`[role="menuitem"]`）。
 */
function menuItem(label: string): HTMLElement | undefined {
  return Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')).find(
    (candidate) => candidate.textContent?.trim() === label,
  )
}

beforeEach(() => {
  // 宽窄是模块级状态：窄屏用例会把它置为 true，不在这里显式复位就会泄漏到
  // 后面的桌面用例。桌面用例（本文件的大多数）因此都从"非窄屏"开始。
  installMatchMedia(false)

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
      project: create(ProjectSchema, {
        id: 'p1',
        name: '我的工程',
        slots: [{ slot: ContentSlot.SITE }],
      }),
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
    create(PreviewDraftResponseSchema, { url: previewURL }),
  )
  // 打开页面就会自动校验一次，因此每个用例都要有一个默认结论；
  // 不补的话 `vi.fn()` 返回 undefined，读 `response.problems` 直接抛。
  vi.mocked(galaxyApi.validateDraft).mockResolvedValue(create(ValidateDraftResponseSchema, {}))
  // 同理，页面挂载即订阅：默认给一条安静的流（见 quietStream）。
  vi.mocked(eventsApi.watchTopics).mockImplementation((_topics, signal) =>
    quietStream(signal),
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
  it('默认落在预览：看得到渲染结果，源码不占版面', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
    )

    const container = await renderEditor()

    expect(container.querySelector('iframe')).not.toBeNull()
    // 预览渲染的是**服务端给的那份 HTML**。
    expect(container.querySelector('iframe')?.getAttribute('src')).toBe(previewURL)
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

    expect(link?.getAttribute('href')).toBe('/galaxy/p1/preview?slot=site')
    expect(link?.getAttribute('target')).toBe('_blank')
  })

  // `site` 槽逐页预览，因此预览侧要能选看哪一页。
  it('站点槽的预览侧有页面选择器，且默认落在入口页', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps(),
    )

    await renderEditor()

    expect(galaxyApi.previewDraft).toHaveBeenCalledWith('p1', ContentSlot.SITE, 'index.html')
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

// 窄屏（手机竖屏）：chrome 折叠，预览/源码成为首屏主体。宽屏形态见上一组——
// 两态引用同一批控件，这里验的是"收进去的那些还到得了"。
describe('窄屏下的形态', () => {
  beforeEach(() => {
    // 晚于外层 beforeEach 执行，因此窄屏在这里生效（外层刚把它复位成桌面）。
    installMatchMedia(true)
  })

  it('常驻行只有 返回 + 标题 + 一颗主操作，集合入口收进「更多」', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps({ publishEnabled: true }))
    vi.mocked(galaxyApi.listVersions).mockResolvedValue(
      create(ListVersionsResponseSchema, {
        versions: [create(VersionSchema, { id: 'v1', seq: 1n })],
      }),
    )

    const container = await renderEditor()

    expect(findButtonByLabel(container, '返回'), '没有返回入口').not.toBeUndefined()
    expect(findButtonByLabel(container, '工程信息'), '标题不在常驻行上').not.toBeUndefined()
    // 主操作常驻：有版本可发布时是「发布」。
    expect(findButton(container, '发布'), '主操作不在常驻行上').not.toBeUndefined()
    // 集合入口不在顶栏的直接可见处。
    expect(findButtonExact(container, '资产'), '资产还在常驻行上').toBeUndefined()
    expect(findButtonExact(container, '版本'), '版本还在常驻行上').toBeUndefined()
    expect(findButtonByLabel(container, '更多'), '没有「更多」入口').not.toBeUndefined()
  })

  it('「更多」里能打开版本面板', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())

    const container = await renderEditor()
    await settle()

    await openMore(container)
    await act(async () => {
      menuItem('版本')?.click()
    })

    expect(document.body.textContent).toContain('序号')
  })

  it('「更多」里能打开资产面板', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())
    vi.mocked(galaxyApi.listAssets).mockResolvedValue(create(ListAssetsResponseSchema, {}))

    const container = await renderEditor()
    await settle()

    await openMore(container)
    await act(async () => {
      menuItem('资产')?.click()
    })

    expect(document.body.textContent).toContain('上传资产')
  })

  it('已发布时状态条一行：短标签可复制、长地址不常驻、撤回仍在', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps({ publishEnabled: true }))
    const publishedURL = 'https://pub.example.com/published/abc'
    vi.mocked(galaxyApi.getProject).mockResolvedValue(
      create(GetProjectResponseSchema, {
        project: create(ProjectSchema, {
          id: 'p1',
          name: '我的工程',
          slots: [{ slot: ContentSlot.SITE, published: true, publishedUrl: publishedURL }],
        }),
      }),
    )

    const container = await renderEditor()
    await settle()

    expect(container.textContent).toContain('已发布')
    // 撤回紧挨着地址、仍是按钮（窄屏只剩图标，名字在 aria-label 上）。
    expect(findButtonByLabel(container, '撤回发布')).not.toBeUndefined()
    // 地址本身不常驻在这条上。
    expect(container.textContent).not.toContain(publishedURL)
  })

  it('预览工具条收起「单独打开」，刷新与预览/源码切换照常', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())

    const container = await renderEditor()

    // 「单独打开」只剩图标：地址没变，只是不再占一段可见文字。
    const openAlone = container.querySelector('[aria-label="单独打开"]')
    expect(openAlone?.getAttribute('href')).toBe('/galaxy/p1/preview?slot=site')
    expect(container.textContent).not.toContain('单独打开')

    expect(findButton(container, '刷新')).not.toBeUndefined()

    await switchStage(container, '源码')
    expect(container.textContent).toContain('index.html')
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

  // **预览的又一条前提是发布域**：它落在发布域上的一条通道上。没有它时不渲染预览
  // 这个模式（只留只读的源码视图），也不去取一条注定取不到的地址——如实缺席，而不是
  // 给一份解析不了自己引用的文档（见 spec 的"没有发布域的部署没有预览"）。
  it('没有发布域时不渲染预览，只留源码视图', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(
      caps({ publishEnabled: false, previewEnabled: false }),
    )

    const container = await renderEditor()

    expect(container.textContent).toContain('这个部署没有发布域，预览不可用')
    expect(container.querySelector('iframe')).toBeNull()
    expect(galaxyApi.previewDraft).not.toHaveBeenCalled()
    // 源码视图照常是当前的模式（它只依赖桶），因此直接就是文件列表。
    expect(container.textContent).toContain('index.html')
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
    expect(galaxyApi.validateDraft).toHaveBeenCalledWith('p1', ContentSlot.SITE)
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

describe('没有"回到前台再读一次"这条兜底', () => {
  function setVisibility(state: DocumentVisibilityState): void {
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => state,
    })
  }

  // 这一条钉住的是一份真实报障：**在预览里点过一下之后，点外壳上任何一个按钮，
  // 预览都会整个重载。** 原因是那条兜底拿"用户是不是又看向这一页"去猜"连接还活着
  // 没有"，于是页内换一次焦点就被当成了回到前台。它现在整条去掉了——判连接的死活
  // 去看连接（通道按心跳判活，见 use-watch.ts），这一页重读只由订阅与用户自己的
  // 动作触发。
  it('页内换焦点、切走再切回来，都不产生任何调用', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())
    const container = await renderEditor()
    await settle()

    const drafts = vi.mocked(galaxyApi.getDraft).mock.calls.length
    const validations = vi.mocked(galaxyApi.validateDraft).mock.calls.length
    const previews = vi.mocked(galaxyApi.previewDraft).mock.calls.length

    // 焦点落进预览那一帧（点内容里的东西），再回到外壳上的某个控件（点按钮）。
    setVisibility('visible')
    await act(async () => {
      window.dispatchEvent(new Event('blur'))
      window.dispatchEvent(new Event('focus'))
    })

    // 切到别的标签页再切回来。
    setVisibility('hidden')
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    setVisibility('visible')
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
      window.dispatchEvent(new Event('focus'))
    })

    expect(vi.mocked(galaxyApi.getDraft).mock.calls.length, '一次焦点/可见性变化触发了重读').toBe(
      drafts,
    )
    expect(vi.mocked(galaxyApi.validateDraft).mock.calls.length).toBe(validations)
    expect(vi.mocked(galaxyApi.previewDraft).mock.calls.length, '预览被重取了一次地址').toBe(
      previews,
    )
    expect(container.querySelector('iframe')?.getAttribute('src')).toBe(previewURL)
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
    // 唯一能触发重读的是别处的改动（订阅事件）。
    const streams: FakeTopicStream[] = []
    vi.mocked(eventsApi.watchTopics).mockImplementation((_topics, signal) => {
      const fake = fakeTopicStream(signal)
      streams.push(fake)
      return fake.stream
    })
    const container = await renderEditor()
    await settle()
    expect(container.textContent).toContain('可以发布')

    vi.mocked(galaxyApi.getDraft).mockRejectedValue(new Error('网络断了'))
    streams[0]?.publish(projectTopic('p1'))
    await settle()

    expect(container.textContent).toContain('校验未完成')
    expect(container.textContent).not.toContain('可以发布')
    expect(container.textContent).toContain('网络断了')
  })
})

describe('订阅推送：别处的改动不用等回到前台', () => {
  it('收到事件即重拉草稿并重新校验，状态条不停在旧结论上', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())

    // 捕捉这一页开的那条流，好在挂载之后往里推事件。
    const streams: FakeTopicStream[] = []
    vi.mocked(eventsApi.watchTopics).mockImplementation((_topics, signal) => {
      const fake = fakeTopicStream(signal)
      streams.push(fake)
      return fake.stream
    })

    await renderEditor()
    // 首屏是一串顺序 await，等它走完再量基线。
    await settle()
    const drafts = vi.mocked(galaxyApi.getDraft).mock.calls.length
    const validations = vi.mocked(galaxyApi.validateDraft).mock.calls.length
    expect(drafts, '首屏没有拉到草稿，这个用例的前提不成立').toBeGreaterThan(0)

    await act(async () => {
      // 首屏可能开过不止一条流：会话权限到达会让加载重跑一次，于是上一条被中止。
      // 推给还活着的那一条——命令行在别处 push 了这个小工程的样子。
      for (const stream of streams) {
        if (!stream.aborted) {
          stream.publish(projectTopic('p1'))
        }
      }
      // 事件是被订阅循环取走的：让它走完，再让重拉那一串 await 走完。
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    expect(
      vi.mocked(galaxyApi.getDraft).mock.calls.length,
      '事件到达后没有重拉草稿',
    ).toBe(drafts + 1)
    expect(
      vi.mocked(galaxyApi.validateDraft).mock.calls.length,
      '事件到达后没有重新问校验，状态条会停在旧结论上',
    ).toBe(validations + 1)
  })

  it('订的是这个工程的主题，而且是它自己那一条', async () => {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())
    await renderEditor()
    await settle()

    // 主题的取值是跨语言的约定：前端这一侧由 watch/topics.ts 拼，服务端那一侧
    // 由 galaxy.ProjectTopic 拼。两边各有一条测试钉住同一个字面量。
    const subscribed = vi.mocked(eventsApi.watchTopics).mock.calls.map(([topics]) => topics)
    expect(subscribed, '这一页没有按工程的主题订阅').toContainEqual(['galaxy.project/p1'])
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

describe('资产的说明层元数据', () => {
  /** 两条资产：一条带标题 / 标签 / 备注，一条只有文件名。 */
  function assetsResponse() {
    return create(ListAssetsResponseSchema, {
      assets: [
        create(AssetSchema, {
          id: 'ast_1',
          mediaType: 'image/png',
          sizeBytes: 3n,
          filename: 'IMG_2031.png',
          uploadedAt: '2026-03-01T12:00:00Z',
          title: '首页封面',
          tags: ['cover', 'hero'],
          notes: '给首页用的图',
        }),
        create(AssetSchema, {
          id: 'ast_2',
          mediaType: 'image/gif',
          sizeBytes: 3n,
          filename: 'other.gif',
          uploadedAt: '2026-03-01T12:00:00Z',
        }),
      ],
      projectTags: ['cover', 'hero'],
    })
  }

  /** 打开资产弹层。 */
  async function openAssets(): Promise<HTMLElement> {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())
    vi.mocked(galaxyApi.listAssets).mockResolvedValue(assetsResponse())
    const container = await renderEditor()
    await settle()
    await clickButton(findButtonExact(container, '资产'), '资产')
    return container
  }

  it('卡片用标题作主标签、没有标题时回退文件名，并展示标签与备注', async () => {
    await openAssets()

    const text = document.body.textContent ?? ''
    expect(text).toContain('首页封面')
    // 有标题时把文件名也留着：否则"这张图是从哪个文件来的"就查不到了。
    expect(text).toContain('文件名：IMG_2031.png')
    expect(text).toContain('cover')
    expect(text).toContain('hero')
    expect(text).toContain('给首页用的图')
    expect(text).toContain('other.gif')
  })

  it('筛选候选是整个工程的标签，改筛选即按新标签重拉', async () => {
    await openAssets()
    // 首屏那一次不筛（不带筛选参数）。
    expect(galaxyApi.listAssets).toHaveBeenCalledWith('p1')

    const selector = document.querySelector('[aria-label="按标签筛选资产"]')
    expect(selector, '没有渲染标签筛选').not.toBeNull()
    await act(async () => {
      selector?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find(
      (candidate) => candidate.textContent === 'cover',
    )
    expect(option, '候选里没有 cover').not.toBeUndefined()
    await act(async () => {
      option?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // 筛选在**服务端**做：选中的标签要随下一次列资产请求带上去。
    expect(galaxyApi.listAssets).toHaveBeenLastCalledWith('p1', ['cover'])
  })

  it('编辑弹层保存整组元数据，成功后重拉清单', async () => {
    await openAssets()
    vi.mocked(galaxyApi.updateAsset).mockResolvedValue(
      create(UpdateAssetResponseSchema, { asset: create(AssetSchema, { id: 'ast_1' }) }),
    )

    await clickButton(findButton(document.body, '编辑'), '编辑')
    const titleInput = document.querySelector('#title') as HTMLInputElement | null
    expect(titleInput, '编辑弹层里没有标题输入框').not.toBeNull()
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value',
      )?.set
      setter?.call(titleInput, '改过的标题')
      titleInput?.dispatchEvent(new Event('input', { bubbles: true }))
    })

    const before = vi.mocked(galaxyApi.listAssets).mock.calls.length
    // antd 会在两个汉字的按钮里插一个空格（"保 存"），因此按去掉空白之后的文本找。
    const saveButton = Array.from(document.body.querySelectorAll('button')).find(
      (candidate) => candidate.textContent?.replace(/\s/g, '') === '保存',
    )
    await clickButton(saveButton, '保存')

    // 期望的是**完整状态**：没改的标签与备注原样带上，而不是只发改过的那一项。
    expect(galaxyApi.updateAsset).toHaveBeenCalledWith(
      'p1',
      'ast_1',
      '改过的标题',
      ['cover', 'hero'],
      '给首页用的图',
    )
    expect(vi.mocked(galaxyApi.listAssets).mock.calls.length).toBeGreaterThan(before)
  })
})

describe('源码视图的文件树与资产呈现', () => {
  /**
   * 一份带目录的草稿：根上是入口页，`img/` 下两份图片（都是资产条目）。
   *
   * 目录这一层是这一组用例的主体——平铺列表里它只是每条路径上的一个前缀。
   */
  function draftWithDirectory() {
    return create(GetDraftResponseSchema, {
      draft: create(DraftSchema, {
        updatedAt: '2026-03-01T12:00:00Z',
        entries: [
          create(FileEntrySchema, {
            path: 'index.html',
            source: { case: 'digest', value: 'aa' },
            url: 'https://cos.example/signed/aa',
          }),
          create(FileEntrySchema, {
            path: 'img/pov-01.jpg',
            source: { case: 'assetId', value: 'ast_1' },
            url: 'https://cos.example/signed/asset-1',
          }),
          create(FileEntrySchema, {
            path: 'img/pov-02.jpg',
            source: { case: 'assetId', value: 'ast_2' },
            url: 'https://cos.example/signed/asset-2',
          }),
        ],
      }),
    })
  }

  /** 一张 jpeg 资产，与草稿里的 `ast_1` 对上。 */
  function assetsWithSequence() {
    return create(ListAssetsResponseSchema, {
      assets: [
        create(AssetSchema, {
          id: 'ast_1',
          mediaType: 'image/jpeg',
          sizeBytes: 3n,
          filename: 'pov-01.jpg',
          uploadedAt: '2026-03-01T12:00:00Z',
        }),
      ],
    })
  }

  /**
   * 左栏里的一行——文本里含 `label` 的**最内层** div。
   *
   * 目录行的外层 div 也含有同样的文本（它把整棵子树包在里面），只看文本会点到
   * 那个不响应的外壳上。
   */
  function findTreeRow(container: HTMLElement, label: string): HTMLElement | undefined {
    const has = (element: HTMLElement): boolean => element.textContent?.includes(label) ?? false
    return Array.from(container.querySelectorAll<HTMLElement>('div'))
      .filter(has)
      .find(
        (candidate) =>
          !Array.from(candidate.querySelectorAll<HTMLElement>('div')).some((child) => has(child)),
      )
  }

  /** 渲染一份带目录的草稿并切到源码视图。 */
  async function renderSource(assets: Asset[]): Promise<HTMLElement> {
    vi.mocked(galaxyApi.getCapabilities).mockResolvedValue(caps())
    vi.mocked(galaxyApi.getDraft).mockResolvedValue(draftWithDirectory())
    vi.mocked(galaxyApi.listAssets).mockResolvedValue(create(ListAssetsResponseSchema, { assets }))
    const container = await renderEditor()
    await settle()
    await switchStage(container, '源码')
    return container
  }

  it('按路径段折成树：文件行只写自己的名字，目录行收着这一支', async () => {
    const container = await renderSource(assetsWithSequence().assets)

    // 目录自己一行——折叠状态挂在它上面，而不是散在每条路径里。
    expect(findTreeRow(container, 'img/')).not.toBeUndefined()
    // 文件行不必再重写目录前缀：目录行已经说了它在哪。
    const fileRow = findTreeRow(container, 'pov-01.jpg')
    expect(fileRow?.textContent).toContain('pov-01.jpg')
    expect(fileRow?.textContent).not.toContain('img/')
  })

  it('折叠一个目录之后，这一支下的文件不再逐条列出', async () => {
    const container = await renderSource(assetsWithSequence().assets)

    await act(async () => {
      findTreeRow(container, 'img/')?.click()
    })
    expect(container.textContent).not.toContain('pov-01.jpg')
    // 折叠的是这一支，别处的文件照常。
    expect(container.textContent).toContain('index.html')
  })

  // 源码视图回答"这一份是什么"；对一张图片，答案就是那张图片。
  it('选中一份资产条目时把它渲染出来，而不是只给一句类型说明', async () => {
    const container = await renderSource(assetsWithSequence().assets)

    await act(async () => {
      findTreeRow(container, 'pov-01.jpg')?.click()
    })

    // 类型与"它是哪一份"压在顶上一行，正文就是那张图本身。
    expect(container.textContent).toContain('image/jpeg')
    // 渲染用的是**条目自己带的**那条短时地址，资产清单取不到时也成立（下一条用例）。
    expect(container.querySelector('img')?.getAttribute('src')).toBe(
      'https://cos.example/signed/asset-1',
    )
  })

  // 缺 `galaxy.asset.read` 时资产清单是空的：说不说得出类型，与给不给得出地址是两件事。
  it('资产清单取不到时说不出类型，但地址照给、不猜着渲染', async () => {
    const container = await renderSource([])

    await act(async () => {
      findTreeRow(container, 'pov-01.jpg')?.click()
    })

    expect(container.textContent).toContain('类型未知')
    // 类型是猜的就不能往 `<img>` 里塞——不认识的格式会显示成一张裂开的图。
    expect(container.querySelector('img')).toBeNull()
    // 地址照给：拿得到原样的字节就还有去处，只是这一页不替它猜类型。
    const link = container.querySelector('[aria-label="在新标签页打开"]')
    expect(link?.getAttribute('href')).toBe('https://cos.example/signed/asset-1')
  })
})
