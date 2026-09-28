import { Code, ConnectError } from '@connectrpc/connect'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as identityApi from '../api/identity'
import * as profileApi from '../api/profile'
import { ProfileProvider } from '../profile'
import { DeviceApprovalPage } from './DeviceApprovalPage'

vi.mock('../api/identity', () => ({
  approveDeviceLogin: vi.fn(),
  denyDeviceLogin: vi.fn(),
}))

vi.mock('../api/profile', () => ({
  getMyProfile: vi.fn(),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ProfileProvider>
        <MemoryRouter initialEntries={['/device']}>
          <DeviceApprovalPage />
        </MemoryRouter>
      </ProfileProvider>,
    )
  })
  return container
}

/** 往受控输入框里写值：直接改 value 不会触发 React 的 onChange。 */
function typeInto(input: Element, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
  setter?.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function buttonByText(container: HTMLElement, text: string): HTMLButtonElement {
  const button = [...container.querySelectorAll('button')].find((candidate) =>
    candidate.textContent?.includes(text),
  )
  if (button === undefined) {
    throw new Error(`找不到「${text}」按钮`)
  }
  return button
}

async function click(button: HTMLButtonElement): Promise<void> {
  await act(async () => {
    button.click()
  })
  // 抗住 antd 校验与请求两段异步。
  await act(async () => {})
}

beforeEach(() => {
  vi.mocked(profileApi.getMyProfile).mockResolvedValue({
    $typeName: 'aladdin.profile.v1.GetProfileResponse',
    profile: {
      $typeName: 'aladdin.profile.v1.Profile',
      nickname: '',
      displayName: '张三',
      bio: '',
      avatarUrl: '',
      avatarUploadEnabled: false,
      avatarMaxBytes: 0,
    },
  })
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
  vi.clearAllMocks()
})

describe('命令行登录的批准页', () => {
  it('显示当前账号：人要知道自己是在以谁的名义批准', async () => {
    const container = await renderPage()

    expect(container.textContent).toContain('张三')
    expect(container.textContent).toContain('只有当你刚刚在这台终端上发起登录时才继续')
  })

  it('批准时把短码原样交给服务端，并告诉使用者可以回到终端', async () => {
    vi.mocked(identityApi.approveDeviceLogin).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.ApproveDeviceLoginResponse',
    })
    const container = await renderPage()

    const input = container.querySelector('input')
    if (input === null) {
      throw new Error('找不到短码输入框')
    }
    await act(async () => {
      typeInto(input, 'BCDF-GHJK')
    })
    await click(buttonByText(container, '批准'))

    expect(identityApi.approveDeviceLogin).toHaveBeenCalledWith('BCDF-GHJK')
    expect(container.textContent).toContain('已批准')
    // 批准之后不该还能再点一次。
    expect(container.querySelector('input')).toBeNull()
  })

  it('拒绝时走拒绝接口，且不产生任何凭证', async () => {
    vi.mocked(identityApi.denyDeviceLogin).mockResolvedValue({
      $typeName: 'aladdin.identity.v1.DenyDeviceLoginResponse',
    })
    const container = await renderPage()

    const input = container.querySelector('input')
    if (input === null) {
      throw new Error('找不到短码输入框')
    }
    await act(async () => {
      typeInto(input, 'BCDF-GHJK')
    })
    await click(buttonByText(container, '拒绝'))

    expect(identityApi.denyDeviceLogin).toHaveBeenCalledWith('BCDF-GHJK')
    expect(identityApi.approveDeviceLogin).not.toHaveBeenCalled()
    expect(container.textContent).toContain('已拒绝')
  })

  it('服务端说代码无效时把它显示出来，而不是静默失败', async () => {
    vi.mocked(identityApi.approveDeviceLogin).mockRejectedValue(
      new ConnectError('这个代码无效或已过期，请在终端上重新发起登录', Code.FailedPrecondition),
    )
    const container = await renderPage()

    const input = container.querySelector('input')
    if (input === null) {
      throw new Error('找不到短码输入框')
    }
    await act(async () => {
      typeInto(input, 'BCDF-GHJK')
    })
    await click(buttonByText(container, '批准'))

    expect(container.textContent).toContain('代码无效或已过期')
    // 失败之后留在原页，可以改一个码重试。
    expect(container.querySelector('input')).not.toBeNull()
  })

  it('账号还没读到时不能批准：人必须先看清是以谁的名义', async () => {
    // 档案一直不返回：账号行停在"读取中"，此时不能让人批准。
    vi.mocked(profileApi.getMyProfile).mockReturnValue(new Promise<never>(() => {}))
    const container = await renderPage()

    expect(container.textContent).toContain('（读取中…）')
    expect(buttonByText(container, '批准').disabled).toBe(true)
    // 拒绝是安全的那一侧：读不到账号不该挡住唯一能挡住这次登录的动作。
    expect(buttonByText(container, '拒绝').disabled).toBe(false)
  })

  it('读不到账号时说明原因并给重试，而不是装作在读', async () => {
    vi.mocked(profileApi.getMyProfile).mockRejectedValue(
      new ConnectError('服务暂时不可用', Code.Unavailable),
    )
    const container = await renderPage()

    expect(container.textContent).toContain('读不到当前账号')
    expect(container.textContent).toContain('服务暂时不可用')
    expect(buttonByText(container, '批准').disabled).toBe(true)
    // 给出重新读取的出口，而不是让人刷新整页。
    buttonByText(container, '重新读取')
  })
})
