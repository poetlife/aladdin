import { create } from '@bufbuild/protobuf'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
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
  ListSkillsResponseSchema,
  SetSkillFavoriteResponseSchema,
  SkillCapabilitiesSchema,
  SkillSchema,
  SkillUsageSchema,
} from '../../gen/proto/aladdin/skill/v1/skill_pb'
import { SkillCatalogPage } from './SkillCatalogPage'

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
  beginCoverUpload: vi.fn(),
  commitCoverUpload: vi.fn(),
  deleteCover: vi.fn(),
  updateCover: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

function skill(id: string, title: string, favorited = false) {
  return create(SkillSchema, {
    id,
    title,
    summary: `${title} 的简介`,
    tags: ['出图'],
    favorited,
    fileCount: 2,
    totalBytes: 128n,
    usage: create(SkillUsageSchema, { useDays: 3, userCount: 2 }),
  })
}

async function renderCatalog(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <SessionProvider>
        <MemoryRouter>
          <SkillCatalogPage />
        </MemoryRouter>
      </SessionProvider>,
    )
  })
  return container
}

function grant(permissions: string[]): void {
  vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
    create(GetSessionPermissionsResponseSchema, { scope: '', permissions }),
  )
}

function buttonWith(container: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(container.querySelectorAll('button')).find((button) =>
    button.textContent?.includes(label),
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  globalThis.localStorage?.clear()
  globalThis.localStorage?.setItem('aladdin.token', 'test-token')
  vi.mocked(identityApi.whoAmI).mockResolvedValue(create(WhoAmIResponseSchema, { subjectId: 's1' }))
  grant([PermissionCodes.SkillCatalogRead])
  vi.mocked(skillApi.getCapabilities).mockResolvedValue(
    create(GetCapabilitiesResponseSchema, {
      capabilities: create(SkillCapabilitiesSchema, {
        catalogEnabled: true,
        importEnabled: true,
        maxFiles: 200,
        maxFileBytes: 256 * 1024,
        maxPackageBytes: 2 * 1024 * 1024,
      }),
    }),
  )
  vi.mocked(skillApi.listSkills).mockResolvedValue(
    create(ListSkillsResponseSchema, {
      skills: [skill('skl_a', '单色出图')],
      availableTags: ['出图', '排版'],
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

describe('目录页', () => {
  it('列出技能，并把使用量与标签显示出来', async () => {
    const container = await renderCatalog()
    expect(container.textContent).toContain('单色出图')
    expect(container.textContent).toContain('出图')
    expect(container.textContent).toContain('最近 30 天：3 个人日 / 2 人')
  })

  // **无写权限的入口不渲染**，而不是渲染一个点了报错的控件（见
  // docs/design/rbac/frontend-permissions.md）。
  it('没有写权限时不渲染纳管入口', async () => {
    const container = await renderCatalog()
    expect(buttonWith(container, '从 GitHub 纳管')).toBeUndefined()
    expect(buttonWith(container, '同步')).toBeUndefined()
  })

  it('有写权限时渲染纳管入口', async () => {
    grant([PermissionCodes.SkillCatalogRead, PermissionCodes.SkillCatalogWrite])
    const container = await renderCatalog()
    expect(buttonWith(container, '从 GitHub 纳管')).not.toBeUndefined()
  })

  it('收藏按钮把期望的完整状态交给服务端，并重拉列表', async () => {
    vi.mocked(skillApi.setFavorite).mockResolvedValue(create(SetSkillFavoriteResponseSchema, {}))
    const container = await renderCatalog()

    const favorite = buttonWith(container, '收藏')
    expect(favorite, '没有找到收藏按钮').not.toBeUndefined()
    await act(async () => {
      favorite?.click()
    })

    expect(skillApi.setFavorite).toHaveBeenCalledWith('skl_a', true)
    expect(skillApi.listSkills).toHaveBeenCalledTimes(2)
  })

  // 目录整体不可用时说明情况，而不是给一排点了报错的按钮。
  it('未配置对象存储时给出说明', async () => {
    vi.mocked(skillApi.getCapabilities).mockResolvedValue(
      create(GetCapabilitiesResponseSchema, {
        capabilities: create(SkillCapabilitiesSchema, { catalogEnabled: false }),
      }),
    )
    const container = await renderCatalog()
    expect(container.textContent).toContain('技能目录不可用')
  })

  // 封面：有图就出图，没有就出占位（首字 + 中性底）。**占位由界面生成**，平台里
  // 没有"默认图"这类二进制资源（见 docs/design/skill/catalog.md 的"封面"）。
  it('有封面时渲染图，没有时渲染占位', async () => {
    vi.mocked(skillApi.listSkills).mockResolvedValue(
      create(ListSkillsResponseSchema, {
        skills: [
          create(SkillSchema, {
            id: 'skl_a',
            title: '单色出图',
            coverUrl: 'https://example.test/c.png',
          }),
          create(SkillSchema, { id: 'skl_b', title: '无封面' }),
        ],
        availableTags: [],
      }),
    )
    const container = await renderCatalog()

    const img = container.querySelector('img')
    expect(img?.getAttribute('src')).toBe('https://example.test/c.png')

    // 占位是图旁边那个 aria-hidden 的块，内容是首字。
    const placeholders = Array.from(container.querySelectorAll('div[aria-hidden]')).filter(
      (node) => node.textContent === '无',
    )
    expect(placeholders).toHaveLength(1)
  })

  it('空目录给出与权限相符的提示', async () => {
    grant([PermissionCodes.SkillCatalogRead, PermissionCodes.SkillCatalogWrite])
    vi.mocked(skillApi.listSkills).mockResolvedValue(
      create(ListSkillsResponseSchema, { skills: [], availableTags: [] }),
    )
    const container = await renderCatalog()
    expect(container.textContent).toContain('目录还是空的')
  })
})
