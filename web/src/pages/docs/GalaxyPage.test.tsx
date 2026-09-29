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

  // 内容是一组具名文件，因此命令行上的输入输出单位是**目录**：写成 --file 那一套
  // 会让人复制到一条不存在的命令。
  it('教的是"目录即整组"，不是一份文件', async () => {
    const container = await renderPage()
    const text = container.textContent ?? ''

    expect(text).toContain('一组具名文件')
    expect(text).toContain('draft push <工程标识> ./dist')
    expect(text).toContain('draft pull')
    expect(text).toContain('version pull')
    // 网页端只读：写入只有命令行一条路。
    expect(text).toContain('网页端只读')
  })

  // 形态创建时定下、此后不可改，这一章必须把两个取值与它们的差别讲清楚。
  it('讲清两种形态的差别', async () => {
    const container = await renderPage()
    const text = container.textContent ?? ''

    expect(text).toContain('static')
    expect(text).toContain('docs')
    expect(text).toContain('index.html')
    expect(text).toContain('index.md')
    expect(text).toContain('不收 HTML')
  })

  // 命令名与位置参数与 cmd/aladdin 一致；写成别的形状会让人复制到一条不存在的命令。
  it('覆盖从建工程到发布的每一步命令', async () => {
    const container = await renderPage()
    const text = container.textContent ?? ''

    for (const command of [
      'aladdin galaxy project create --form',
      'aladdin galaxy project base <工程标识>',
      'aladdin galaxy asset upload <工程标识>',
      'aladdin galaxy asset list <工程标识>',
      'aladdin galaxy draft push <工程标识>',
      'aladdin galaxy validate <工程标识>',
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
