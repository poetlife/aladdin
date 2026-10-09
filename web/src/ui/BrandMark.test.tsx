import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import markDark from '../brand/mark-dark.svg'
import markLight from '../brand/mark.svg'
import { ThemeProvider } from '../theme'
import { BrandMark } from './BrandMark'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

/** 在给定主题偏好下渲染一次标识，返回 <img> 上的 src。 */
async function srcUnder(preference: 'light' | 'dark'): Promise<string> {
  globalThis.localStorage.setItem('aladdin.theme', preference)

  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <ThemeProvider>
        <BrandMark />
      </ThemeProvider>,
    )
  })

  const image = container.querySelector('img')
  expect(image, '没有渲染出标识').not.toBeNull()
  return image?.getAttribute('src') ?? ''
}

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
  globalThis.localStorage.clear()
})

describe('品牌标识', () => {
  // 暗色版不是"同一张图调暗"：它换了渐变的取值，并且多一层发光。挑错版本的表现
  // 不是报错，而是深色标签栏上浮着一条发白的形状——那要有人正好在暗色下打开才看得见。
  //
  // 断言比的是**导入进来的那两份资源本身**，不是 src 里的字样：构建会把小 SVG 内联成
  // data URI，于是"src 里有没有 mark-dark 这几个字"这判断既看错了地方（看的是一整段
  // SVG 源码），又会随注释里的字样变化——一条会自己变成假警报的断言。
  it('跟着主题取对应那一版', async () => {
    const light = await srcUnder('light')
    expect(light, '浅色主题取到了暗色版').toBe(markLight)

    await act(async () => {
      root?.unmount()
    })
    document.body.replaceChildren()

    const dark = await srcUnder('dark')
    expect(dark, '暗色主题取到了浅色版').toBe(markDark)
  })

  // 旁边已经有文字写着这是哪个站时，这一份是装饰——读屏软件不该把同一个名字念两遍。
  it('没有给名字时是装饰性的', async () => {
    await srcUnder('light')
    expect(document.querySelector('img')?.getAttribute('alt')).toBe('')
  })

  it('给了名字就把它带上', async () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    await act(async () => {
      root?.render(
        <ThemeProvider>
          <BrandMark label="阿拉丁神灯" />
        </ThemeProvider>,
      )
    })

    // 导轨收起态就是这样：旁边没有文字，标识成了唯一说明"这是哪个站"的东西。
    expect(container.querySelector('img')?.getAttribute('alt')).toBe('阿拉丁神灯')
  })
})
