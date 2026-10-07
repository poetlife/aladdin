import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import { IdentityCard } from './identity-card'

vi.mock('../api/identity', () => ({
  AuthSource: { Google: 'google', Github: 'github' },
  getAuthMethods: vi.fn(),
  listIdentities: vi.fn(),
  unbindIdentity: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

/** 渲染「登录方式」卡片；state 模拟回调页带回来的绑定结果。 */
async function renderCard(state: unknown = null): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <MemoryRouter initialEntries={[{ pathname: '/profile', state }]}>
        <IdentityCard />
      </MemoryRouter>,
    )
  })
  return container
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(identityApi.listIdentities).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.ListIdentitiesResponse',
    identities: [],
  })
  vi.mocked(identityApi.getAuthMethods).mockResolvedValue([
    {
      $typeName: 'aladdin.identity.v1.AuthMethod',
      source: identityApi.AuthSource.Github,
    },
  ])
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

it('未绑定的 GitHub 给出去绑定的起点链接', async () => {
  const container = await renderCard()

  expect(container.querySelector('a[href="/auth/github/start?purpose=bind"]')).not.toBeNull()
})

it('未绑定的 Google 也给出去绑定的起点链接，并带绑定用途标记', async () => {
  vi.mocked(identityApi.getAuthMethods).mockResolvedValue([
    {
      $typeName: 'aladdin.identity.v1.AuthMethod',
      source: identityApi.AuthSource.Google,
    },
  ])

  const container = await renderCard()

  // purpose 只告诉服务端这次导航走向绑定分支，绑到谁在回跳后的已认证兑换里决定。
  expect(container.querySelector('a[href="/auth/google/start?purpose=bind"]')).not.toBeNull()
  expect(container.querySelector('a[href="/auth/github/start?purpose=bind"]')).toBeNull()
})

it('已绑定的渠道只展示，不再给绑定入口', async () => {
  vi.mocked(identityApi.listIdentities).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.ListIdentitiesResponse',
    identities: [
      {
        $typeName: 'aladdin.identity.v1.Identity',
        source: identityApi.AuthSource.Github,
        externalId: '1001',
        display: 'octo',
      },
    ],
  })

  const container = await renderCard()

  expect(container.querySelector('a[href="/auth/github/start?purpose=bind"]')).toBeNull()
  expect(container.textContent).toContain('octo')
})

it('回调带回的认领结果给出「已并入」提示', async () => {
  const container = await renderCard({ identityBound: 'github', reclaimed: true })

  expect(container.textContent).toContain('已把此前单独登录过')
})

// 读到之前给骨架，而不是先写一句"还没有绑定任何登录方式"再把它换掉——
// 那句话在加载期间是错的。
it('读到之前给骨架，不给一个空的结论', async () => {
  vi.mocked(identityApi.listIdentities).mockReturnValue(new Promise<never>(() => {}))

  const container = await renderCard()

  expect(container.querySelector('.ant-skeleton')).not.toBeNull()
  expect(container.textContent).not.toContain('还没有绑定任何登录方式')
})

// 读不到现状时必须给出出路，而不是永远停在加载骨架里。
it('读不到现状时给出重试入口，而不是停在加载态', async () => {
  vi.mocked(identityApi.listIdentities).mockRejectedValue(new Error('服务不可用'))

  const container = await renderCard()

  expect(container.querySelector('.ant-skeleton')).toBeNull()
  // 按容器选而不是按文案：antd 会在两个汉字之间插空格（"重 试"）。
  const retry = container.querySelector<HTMLButtonElement>('.ant-alert-actions button')
  expect(retry).not.toBeNull()

  // 重试之后恢复正常，而不是只把错误清掉。
  vi.mocked(identityApi.listIdentities).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.ListIdentitiesResponse',
    identities: [],
  })
  await act(async () => {
    retry?.click()
  })
  expect(container.textContent).toContain('还没有绑定任何登录方式')
})

// 上一次的成功提示只保留到下一次操作：解绑之后它说的就是一件不再成立的事。
it('下一次操作清掉上一次的绑定提示', async () => {
  vi.mocked(identityApi.listIdentities).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.ListIdentitiesResponse',
    identities: [
      {
        $typeName: 'aladdin.identity.v1.Identity',
        source: identityApi.AuthSource.Github,
        externalId: '1001',
        display: 'octo',
      },
      {
        $typeName: 'aladdin.identity.v1.Identity',
        source: identityApi.AuthSource.Google,
        externalId: 'g-1',
        display: 'g@example.com',
      },
    ],
  })
  vi.mocked(identityApi.unbindIdentity).mockResolvedValue({
    $typeName: 'aladdin.identity.v1.UnbindIdentityResponse',
    identities: [],
  })

  const container = await renderCard({ identityBound: 'github', reclaimed: true })
  expect(container.textContent).toContain('已把此前单独登录过')

  // 解绑是一次"下一次操作"：点开确认气泡，再点它的确认按钮。
  const trigger = container.querySelector<HTMLButtonElement>('.ant-listy-item button')
  await act(async () => {
    trigger?.click()
  })
  const confirm = document.querySelector<HTMLButtonElement>('.ant-popover .ant-btn-primary')
  expect(confirm).not.toBeNull()
  await act(async () => {
    confirm?.click()
  })

  expect(identityApi.unbindIdentity).toHaveBeenCalled()
  expect(container.textContent).not.toContain('已把此前单独登录过')
})
