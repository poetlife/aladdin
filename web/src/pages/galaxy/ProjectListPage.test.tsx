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
  DeleteProjectResponseSchema,
  ListProjectsResponseSchema,
  ProjectSchema,
  SiteForm,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { ProjectListPage } from './ProjectListPage'

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
      projects: [create(ProjectSchema, { id: 'p1', name: '我的工程', form: SiteForm.STATIC })],
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
