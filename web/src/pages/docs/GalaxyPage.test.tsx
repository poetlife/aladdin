import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import { GalaxyPage } from './GalaxyPage'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<GalaxyPage />)
  })
  return container
}

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('创作与发布介绍页', () => {
  // 这一章要教的核心事实：正文里引用素材写的是记号，真实地址（含访问签名）由系统
  // 在预览与发布两处补上。示例正文里出现一个具体地址，就把这句话说反了。
  it('教的是 asset:// 记号，而不是把真实地址写进正文', async () => {
    const container = await renderPage()
    const text = container.textContent ?? ''

    expect(text).toContain('asset://')
    expect(text).toContain('没有真实地址')
    // 记号是拿来引用素材的，得说清它指向的是"资产标识"。
    expect(text).toContain('<资产标识>')
  })

  it('说明补地址分为预览与发布两处', async () => {
    const container = await renderPage()
    const text = container.textContent ?? ''

    expect(text).toContain('预览')
    expect(text).toContain('预签名')
    expect(text).toContain('公开地址')
  })

  // 命令名与位置参数与 cmd/aladdin 一致；写成别的形状会让人复制到一条不存在的命令。
  it('覆盖从建工程到发布的每一步命令', async () => {
    const container = await renderPage()
    const text = container.textContent ?? ''

    for (const command of [
      'aladdin galaxy project create --name',
      'aladdin galaxy asset upload <工程标识>',
      'aladdin galaxy asset list <工程标识>',
      'aladdin galaxy draft save <工程标识> --file',
      'aladdin galaxy validate <工程标识> --file',
      'aladdin galaxy version save <工程标识>',
      'aladdin galaxy publish <工程标识> <版本标识> --yes',
    ]) {
      expect(text, `正文里少了 ${command}`).toContain(command)
    }
  })

  // 发布是唯一让内容离开私有边界的动作（见 docs/design/galaxy/cli.md），
  // 脚本里必须显式 --yes；而撤回发布不在危险集合里，这一章不能把两者混为一谈。
  it('点明危险操作要 --yes，且说明撤回发布不算危险操作', async () => {
    const container = await renderPage()
    const text = container.textContent ?? ''

    expect(text).toContain('--yes')
    expect(text).toContain('unpublish')
    expect(text).toContain('不是')
  })

  // 发布域是部署实例的值，写进仓库就违反"仓库不含实例值"（见 docs/deploy.md）。
  // 这一页通篇在讲地址，正是最容易顺手写死一个域名的地方。
  it('不出现任何绝对地址', async () => {
    const container = await renderPage()

    expect(container.textContent ?? '').not.toMatch(/https?:\/\//)
  })
})
