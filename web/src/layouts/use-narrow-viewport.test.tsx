import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { installMatchMedia, setNarrow } from '../test/match-media'
import { useNarrowViewport } from './use-narrow-viewport'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

/** 把 hook 的返回值原样画出来，断言就看这段文字。 */
function Probe(): React.ReactNode {
  return <span>{String(useNarrowViewport())}</span>
}

let root: Root | null = null

async function renderProbe(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<Probe />)
  })
  return container
}

beforeEach(() => {
  installMatchMedia(false)
})

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
})

describe('窄屏判定', () => {
  it('首帧就按当前视口给结论，不先给一个默认值再改', async () => {
    installMatchMedia(true)

    const container = await renderProbe()

    expect(container.textContent).toBe('true')
  })

  it('视口在运行期变化时跟着变', async () => {
    const container = await renderProbe()
    expect(container.textContent).toBe('false')

    await act(async () => {
      setNarrow(true)
    })

    expect(container.textContent).toBe('true')

    // 反向也要能变回去：只订阅、忘了退订的话，这里会停在 true。
    await act(async () => {
      setNarrow(false)
    })

    expect(container.textContent).toBe('false')
  })
})
