import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import { CliPage } from './CliPage'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderPage(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<CliPage />)
  })
  return container
}

/** 点第 index 个标签页（0 起）。antd 的标签页是懒渲染的，不点就取不到内容。 */
async function clickTab(container: HTMLElement, index: number): Promise<void> {
  const tabs = Array.from(container.querySelectorAll('[role="tab"]'))
  expect(tabs[index], `没有第 ${index + 1} 个标签页`).not.toBeUndefined()
  await act(async () => {
    tabs[index]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('命令行介绍页', () => {
  // 产物名里带着 tag。写死一个版本号，下一个 tag 之后那条命令就指向一个不存在
  // 的产物——发布流程不会因此失败，页面也不会报错，只有一个复制了命令的人
  // 在终端里看到 404（见 docs/design/cli/install.md）。
  it('不出现版本号字面量', async () => {
    const container = await renderPage()

    expect(container.textContent ?? '').not.toMatch(/\bv\d+\.\d+\.\d+\b/)
  })

  it('下载命令在运行期解析最新 tag，而不是写死一个', async () => {
    const container = await renderPage()
    const text = container.textContent ?? ''

    expect(text).toContain('releases/latest')
    expect(text).toContain('tag=${tag##*/}')
    expect(text).toContain('aladdin_${tag}_darwin_arm64.tar.gz')
  })

  it('两个平台都可选，各自的产物名与校验工具都对', async () => {
    const container = await renderPage()

    expect(container.textContent).toContain('macOS（Apple Silicon）')
    expect(container.textContent).toContain('Linux（x86-64）')
    expect(container.textContent).toContain('shasum -a 256 -c -')
    // 未选中的标签页还没渲染，先确认它此刻确实不在。
    expect(container.textContent).not.toContain('aladdin_${tag}_linux_amd64.tar.gz')

    await clickTab(container, 1)

    expect(container.textContent).toContain('aladdin_${tag}_linux_amd64.tar.gz')
    expect(container.textContent).toContain('sha256sum -c -')
  })

  // 这些命令要么长、要么多行，手动选中容易漏掉续行。见 install.md 第 5 条。
  it('每个命令块都自带复制入口', async () => {
    const container = await renderPage()
    await clickTab(container, 1)

    const blocks = Array.from(container.querySelectorAll('pre'))
    expect(blocks.length).toBeGreaterThan(1)
    for (const block of blocks) {
      const wrapper = block.parentElement
      expect(
        wrapper?.querySelector('button.ant-typography-copy'),
        `命令块没有复制入口：${block.textContent?.slice(0, 30)}`,
      ).not.toBeNull()
    }
  })

  // 非特权用户装 /usr/local/bin 会 Permission denied；装得上的人此后每次升级也要
  // sudo——而升级正是自更新要免掉的那个人工步骤（见 install.md「装在哪里」）。
  // 这里钉住**落点**本身，而不是禁止某个字符串：那句"别装系统目录"的解释值得留在
  // 命令旁边的注释里。
  it('两个平台的安装落点都是用户可写目录', async () => {
    const container = await renderPage()
    await clickTab(container, 1)

    const installs = Array.from(container.querySelectorAll('pre'))
      .map((block) => /^install -m 0755 aladdin (\S+)$/m.exec(block.textContent ?? '')?.[1])
      .filter((dest): dest is string => dest !== undefined)

    expect(installs).toEqual(['~/.local/bin/aladdin', '~/.local/bin/aladdin'])
  })
})
