import { create } from '@bufbuild/protobuf'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../../api/identity'
import * as skillApi from '../../api/skill'
import { SessionProvider } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import {
  GetSessionPermissionsResponseSchema,
  WhoAmIResponseSchema,
} from '../../gen/proto/aladdin/identity/v1/identity_pb'
import {
  GetCapabilitiesResponseSchema,
  GetSkillFileResponseSchema,
  GetSkillResponseSchema,
  ListSkillVersionsResponseSchema,
  SkillCapabilitiesSchema,
  SkillFileSchema,
  SkillImageSchema,
  SkillSchema,
  SkillSourceSchema,
  SkillUsageSchema,
  SkillVersionSchema,
} from '../../gen/proto/aladdin/skill/v1/skill_pb'
import { SkillDetailPage } from './SkillDetailPage'

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

vi.mock('../../api/skill', () => ({
  getCapabilities: vi.fn(),
  listSkills: vi.fn(),
  getSkill: vi.fn(),
  getSkillFile: vi.fn(),
  listSkillVersions: vi.fn(),
  setFavorite: vi.fn(),
  importSkill: vi.fn(),
  resyncSkill: vi.fn(),
  setCurrentVersion: vi.fn(),
  updateMetadata: vi.fn(),
  deleteSkill: vi.fn(),
  beginImageUpload: vi.fn(),
  commitImageUpload: vi.fn(),
  deleteSkillImage: vi.fn(),
  reorderSkillImages: vi.fn(),
  addSkillImage: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderDetail(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <MemoryRouter initialEntries={['/skills/skl_a']}>
          <Routes>
            <Route path="/skills/:skillId" element={<SkillDetailPage />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>,
    )
  })
  return container
}

/**
 * 一份技能的详情响应。`images` 是**有序**的展示图集，第一张即首图——因此 coverUrl
 * 跟着它走：列表与详情都给首图，服务端就是这么下发的。
 */
function detail(images: ReturnType<typeof image>[] = []) {
  return create(GetSkillResponseSchema, {
    skill: create(SkillSchema, {
      id: 'skl_a',
      title: '单色出图',
      titleOverride: '',
      summary: '单色印刷风。',
      summaryOverride: '',
      name: 'mono-color',
      description: '当用户要一张克制的海报时使用。',
      tags: ['出图'],
      currentVersionId: 'skv_1',
      currentVersionCreatedAt: '2026-10-06T12:00:00Z',
      source: create(SkillSourceSchema, {
        repositoryUrl: 'https://github.com/yanliudesign/mono-color-skill',
        ref: 'main',
        commit: '1f4a9c2d3e5b6a7089abcdef1234567890abcdef',
      }),
      usage: create(SkillUsageSchema, { useDays: 0, userCount: 0 }),
      fileCount: 2,
      totalBytes: 128n,
      files: [
        create(SkillFileSchema, { path: 'SKILL.md', sizeBytes: 100n, digest: 'a'.repeat(64) }),
        create(SkillFileSchema, { path: 'palette.md', sizeBytes: 1536n, digest: 'b'.repeat(64) }),
      ],
      images,
      coverUrl: images[0]?.url ?? '',
    }),
  })
}

/** 图集里的一张。地址是短时预签名地址，测试里只要它逐张不同就够。 */
function image(id: string) {
  return create(SkillImageSchema, { id, url: `https://example.test/${id}.png`, sizeBytes: 1024n })
}

/** 给这一次渲染加上写权限（维护动作要 skill.catalog.write）。 */
function grantWrite(): void {
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
    create(GetSessionPermissionsResponseSchema, {
      scope: '',
      permissions: [PermissionCodes.SkillCatalogRead, PermissionCodes.SkillCatalogWrite],
    }),
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  globalThis.localStorage?.clear()
  globalThis.localStorage?.setItem('aladdin.token', 'test-token')
  vi.mocked(identityApi.whoAmI).mockResolvedValue(create(WhoAmIResponseSchema, { subjectId: 's1' }))
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
    create(GetSessionPermissionsResponseSchema, {
      scope: '',
      permissions: [PermissionCodes.SkillCatalogRead],
    }),
  )
  vi.mocked(skillApi.getSkill).mockResolvedValue(detail())
  // 图集上限**故意与服务端的默认值（12 张 / 2 MiB / 12 MiB）都不一样**：屏幕上出现的
  // 数字若来自这里，就说明它不是前端写死的。
  vi.mocked(skillApi.getCapabilities).mockResolvedValue(
    create(GetCapabilitiesResponseSchema, {
      capabilities: create(SkillCapabilitiesSchema, {
        catalogEnabled: true,
        importEnabled: true,
        maxImages: 5,
        maxImageBytes: 1024 * 1024,
        maxImageTotalBytes: 4 * 1024 * 1024,
      }),
    }),
  )
  vi.mocked(skillApi.listSkillVersions).mockResolvedValue(
    create(ListSkillVersionsResponseSchema, {
      versions: [
        create(SkillVersionSchema, {
          id: 'skv_2',
          commit: 'abcdef1234567890abcdef1234567890abcdef12',
          fileCount: 2,
          skippedFiles: 26,
          current: true,
          createdAt: '2026-10-06T13:00:00Z',
        }),
        create(SkillVersionSchema, {
          id: 'skv_1',
          commit: '1f4a9c2d3e5b6a7089abcdef1234567890abcdef',
          fileCount: 2,
          current: false,
          createdAt: '2026-10-06T12:00:00Z',
        }),
      ],
    }),
  )
  vi.mocked(skillApi.getSkillFile).mockResolvedValue(
    create(GetSkillFileResponseSchema, {
      path: 'SKILL.md',
      digest: 'a'.repeat(64),
      content: new TextEncoder().encode('# 说明\n正文\n'),
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

describe('详情页', () => {
  it('显示说明层、触发说明与来源', async () => {
    const container = await renderDetail()
    expect(container.textContent).toContain('单色出图')
    expect(container.textContent).toContain('当用户要一张克制的海报时使用。')
    expect(container.textContent).toContain('https://github.com/yanliudesign/mono-color-skill')
    expect(container.textContent).toContain('1f4a9c2d3e5b')
  })

  // **进页面不取正文**：取用计一次使用，而"翻一翻"与"用一下"是两件事。
  it('不在进页面时取正文', async () => {
    await renderDetail()
    expect(skillApi.getSkillFile).not.toHaveBeenCalled()
  })

  it('点开一份文件才取正文，并按等宽纯文本显示', async () => {
    const container = await renderDetail()
    const entry = Array.from(container.querySelectorAll('li')).find((item) =>
      item.textContent?.includes('palette.md'),
    )
    expect(entry, '没有找到文件条目').not.toBeUndefined()
    // 大小走 format/bytes 的那一个入口，而不是在这里另写一遍：1536 字节说「2 KB」
    // （原样打字节数会是「1536 B」，这条就是照那个写的）。
    expect(entry?.textContent).toContain('2 KB')
    // 小于 1 KiB 的那一档说字节数，不折成「0 KB」——技能包里最常见的就是它。
    const manifest = Array.from(container.querySelectorAll('li')).find((item) =>
      item.textContent?.includes('SKILL.md'),
    )
    expect(manifest?.textContent).toContain('100 B')

    await act(async () => {
      entry?.click()
    })

    expect(skillApi.getSkillFile).toHaveBeenCalledWith('skl_a', 'palette.md')
    const pre = container.querySelector('pre')
    expect(pre?.textContent).toContain('正文')
    // 不渲染 markdown：正文原样进 pre，页面上没有 markdown 转换出来的标签。
    expect(pre?.querySelector('h1')).toBeNull()
  })

  // 没有写权限时，版本卡、说明层动作与图集的管理动作一律不渲染。
  it('没有写权限时不渲染版本卡与维护动作', async () => {
    vi.mocked(skillApi.getSkill).mockResolvedValue(detail([image('img_a'), image('img_b')]))
    const container = await renderDetail()
    expect(container.textContent).not.toContain('切到这一版')
    expect(container.textContent).not.toContain('改说明层')
    expect(container.textContent).not.toContain('加图')
    expect(container.querySelectorAll('button[aria-label="设为首图"]')).toHaveLength(0)
    expect(container.querySelectorAll('button[aria-label="上移"]')).toHaveLength(0)
    expect(container.querySelectorAll('button[aria-label="下移"]')).toHaveLength(0)
    expect(container.querySelectorAll('button[aria-label="删除这一张"]')).toHaveLength(0)
    // 图本身照常看得见：看不需要写权限。
    expect(container.querySelectorAll('img')).toHaveLength(3)
  })

  // 空图集时沿用占位（标题首字 + 中性底），**不引入默认图**：平台里没有这类
  // 二进制资源（见 docs/design/skill/catalog.md 的"展示图集"）。
  it('空图集渲染占位，并给出加图入口', async () => {
    grantWrite()
    const container = await renderDetail()

    expect(container.querySelectorAll('img')).toHaveLength(0)
    const placeholder = Array.from(container.querySelectorAll('div[aria-hidden]')).find(
      (node) => node.textContent === '单',
    )
    expect(placeholder, '没有找到占位').not.toBeUndefined()
    expect(container.textContent).toContain('加图')
    // 上限**来自能力下发**，且这里逐字断言那一句：写死 12 张 / 2 MiB / 12 MiB 就过不了。
    expect(container.textContent).toContain('最多 5 张，单张不超过 1 MiB、合计不超过 4 MiB。')
  })

  // 上限还没读到（能力这次没回来）时那一句不渲染：说一个猜的上限，不如不说。
  it('能力还没到时不说图集上限', async () => {
    grantWrite()
    vi.mocked(skillApi.getCapabilities).mockReturnValue(new Promise<never>(() => {}))
    const container = await renderDetail()

    expect(container.textContent).toContain('加图')
    expect(container.textContent).not.toContain('单张不超过')
  })

  // 图集是**有序**的：第一张即首图（也就是大图），缩略图按同一顺序跟在后面。
  it('多张图按顺序渲染', async () => {
    grantWrite()
    vi.mocked(skillApi.getSkill).mockResolvedValue(
      detail([image('img_a'), image('img_b'), image('img_c')]),
    )
    const container = await renderDetail()

    const srcs = Array.from(container.querySelectorAll('img')).map((node) =>
      node.getAttribute('src'),
    )
    expect(srcs).toEqual([
      'https://example.test/img_a.png', // 大图就是首图
      'https://example.test/img_a.png',
      'https://example.test/img_b.png',
      'https://example.test/img_c.png',
    ])
  })

  it('点「设为首图」把那一张排到第一位，给出的是期望的完整顺序', async () => {
    grantWrite()
    vi.mocked(skillApi.getSkill).mockResolvedValue(
      detail([image('img_a'), image('img_b'), image('img_c')]),
    )
    const container = await renderDetail()

    const stars = Array.from(
      container.querySelectorAll<HTMLButtonElement>('button[aria-label="设为首图"]'),
    )
    expect(stars).toHaveLength(3)
    // 第一张已经是首图：这一颗禁掉，而不是等人点了再报错（见 docs/design/uiux/README.md）。
    expect(stars[0]?.disabled).toBe(true)

    await act(async () => {
      stars[2]?.click()
    })

    // 整体替换、第一项即首图——不是"上移一位"这类增量动作。
    expect(skillApi.reorderSkillImages).toHaveBeenCalledWith('skl_a', ['img_c', 'img_a', 'img_b'])
  })

  it('上移 / 下移与相邻一张对调，给出的是交换之后的完整顺序', async () => {
    grantWrite()
    vi.mocked(skillApi.getSkill).mockResolvedValue(
      detail([image('img_a'), image('img_b'), image('img_c')]),
    )
    const container = await renderDetail()

    const ups = Array.from(
      container.querySelectorAll<HTMLButtonElement>('button[aria-label="上移"]'),
    )
    const downs = Array.from(
      container.querySelectorAll<HTMLButtonElement>('button[aria-label="下移"]'),
    )
    expect(ups).toHaveLength(3)
    expect(downs).toHaveLength(3)
    // 第一张没有上一张、最后一张没有下一张：各自禁掉，而不是等人点了再报错。
    expect(ups[0]?.disabled).toBe(true)
    expect(downs[2]?.disabled).toBe(true)

    // 把第 1 张下移：与第 2 张对调。
    await act(async () => {
      downs[0]?.click()
    })
    // 把第 3 张上移：与第 2 张对调。
    await act(async () => {
      ups[2]?.click()
    })

    // 发出的是**整个顺序**（与设为首图同一个出口），不是"交换哪两项"的增量。
    expect(skillApi.reorderSkillImages).toHaveBeenNthCalledWith(1, 'skl_a', [
      'img_b',
      'img_a',
      'img_c',
    ])
    expect(skillApi.reorderSkillImages).toHaveBeenNthCalledWith(2, 'skl_a', [
      'img_a',
      'img_c',
      'img_b',
    ])
  })

  it('删一张要二次确认，确认后按标识调 deleteSkillImage', async () => {
    grantWrite()
    vi.mocked(skillApi.getSkill).mockResolvedValue(detail([image('img_a'), image('img_b')]))
    const container = await renderDetail()

    const deletes = Array.from(
      container.querySelectorAll<HTMLButtonElement>('button[aria-label="删除这一张"]'),
    )
    expect(deletes).toHaveLength(2)

    await act(async () => {
      deletes[1]?.click()
    })
    await act(async () => {})
    const confirm = document.querySelector<HTMLButtonElement>('.ant-popconfirm .ant-btn-primary')
    expect(confirm, '确认框里没有确认按钮').not.toBeNull()
    await act(async () => {
      confirm?.click()
    })

    // 按**标识**指认（位置会随重排变，标识不会），且删的不是首图。
    expect(skillApi.deleteSkillImage).toHaveBeenCalledWith('skl_a', 'img_b')
  })

  it('选一个文件加图', async () => {
    grantWrite()
    const container = await renderDetail()

    const input = container.querySelector<HTMLInputElement>('input[type="file"]')
    expect(input, '没有找到上传入口').not.toBeNull()
    const file = new File([new Uint8Array([1, 2, 3])], 'cover.png', { type: 'image/png' })
    await act(async () => {
      Object.defineProperty(input, 'files', { value: [file] })
      input?.dispatchEvent(new Event('change', { bubbles: true }))
    })

    // 三步（签发 → 直传 → 提交）收在 addSkillImage 里，它自己另有一条单测。
    expect(skillApi.addSkillImage).toHaveBeenCalledWith('skl_a', file)
  })

  it('有写权限时列出全部版本并标出当前那一版', async () => {
    grantWrite()
    const container = await renderDetail()
    expect(container.textContent).toContain('skv_1')
    expect(container.textContent).toContain('skv_2')
    expect(container.textContent).toContain('当前')
    expect(container.textContent).toContain('切到这一版')
    // 平台只收文本，上游的示例图一类不进包——**这件事必须看得见**。
    expect(container.textContent).toContain('上游另有 26 个条目未收')
  })
})
