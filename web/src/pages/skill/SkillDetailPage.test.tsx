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
  GetSkillFileResponseSchema,
  GetSkillResponseSchema,
  ListSkillVersionsResponseSchema,
  SkillFileSchema,
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
  beginCoverUpload: vi.fn(),
  commitCoverUpload: vi.fn(),
  deleteCover: vi.fn(),
  updateCover: vi.fn(),
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
  vi.mocked(skillApi.getSkill).mockResolvedValue(
    create(GetSkillResponseSchema, {
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

  // 没有写权限时，版本卡与维护动作一律不渲染。
  it('没有写权限时不渲染版本卡与维护动作', async () => {
    const container = await renderDetail()
    expect(container.textContent).not.toContain('切到这一版')
    expect(container.textContent).not.toContain('改说明层')
  })

  it('有写权限时能加封面；这一份没有封面，就不给「移除封面」', async () => {
    vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
      create(GetSessionPermissionsResponseSchema, {
        scope: '',
        permissions: [PermissionCodes.SkillCatalogRead, PermissionCodes.SkillCatalogWrite],
      }),
    )
    const container = await renderDetail()
    // 没有封面时这颗按钮说的是它真正会做的事；有封面才叫「换封面」。
    expect(container.textContent).toContain('加一张封面')
    expect(container.textContent).not.toContain('移除封面')
  })

  it('有写权限时列出全部版本并标出当前那一版', async () => {
    vi.mocked(identityApi.getSessionPermissions).mockResolvedValue(
      create(GetSessionPermissionsResponseSchema, {
        scope: '',
        permissions: [PermissionCodes.SkillCatalogRead, PermissionCodes.SkillCatalogWrite],
      }),
    )
    const container = await renderDetail()
    expect(container.textContent).toContain('skv_1')
    expect(container.textContent).toContain('skv_2')
    expect(container.textContent).toContain('当前')
    expect(container.textContent).toContain('切到这一版')
    // 平台只收文本，上游的示例图一类不进包——**这件事必须看得见**。
    expect(container.textContent).toContain('上游另有 26 个条目未收')
  })
})
