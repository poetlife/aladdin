import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { FRAME_CHANNEL, FRAME_CHANNEL_VERSION } from './frame-channel'
import { SandboxFrame } from './SandboxFrame'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function renderFrame(url: string): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(<SandboxFrame url={url} title="发布页" />)
  })
  return container
}

/** 换一条地址重渲染，模拟预览被重新取了一次。 */
async function rerenderFrame(url: string): Promise<void> {
  await act(async () => {
    root?.render(<SandboxFrame url={url} title="发布页" />)
  })
}

function iframe(container: HTMLElement): HTMLIFrameElement {
  const element = container.querySelector('iframe')
  expect(element, '没有渲染出 iframe').not.toBeNull()
  return element as HTMLIFrameElement
}

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

describe('沙箱 iframe', () => {
  // spec 明确要求的核查项：内容落在不透明源上，沙箱属性里不得出现 allow-same-origin。
  // 预览与主站壳共用这一处实现，因此这条断言对两条路都成立。
  it('沙箱属性里没有 allow-same-origin', async () => {
    const container = await renderFrame('https://pub.example.com/g/prj_x/index.html')

    const sandbox = iframe(container).getAttribute('sandbox') ?? ''
    expect(sandbox).not.toContain('allow-same-origin')
    // 脚本仍要能跑（内容里可能有用户自己的脚本），因此 allow-scripts 必须在。
    expect(sandbox).toContain('allow-scripts')
  })

  // 加载的是**一条地址**而不是塞进来的一份 HTML：页面因此有自己的源与目录，
  // 页内的相对地址与站点绝对地址都由浏览器自己解析（见 authoring.md 的"预览"）。
  // 地址由服务端给出，前端原样使用，不拼、不改写。
  it('服务端给的地址原样进 src', async () => {
    const url = 'https://pub.example.com/g/prj_x/index.html'

    const container = await renderFrame(url)

    const element = iframe(container)
    expect(element.getAttribute('src')).toBe(url)
    // 塞一份 HTML 的那条路已经不存在了。
    expect(element.getAttribute('srcdoc')).toBeNull()
  })

  // 无标题的 iframe 对无障碍树不可见：页面上的 frame 得有可区分的名字。
  it('标题进到 iframe 上', async () => {
    const container = await renderFrame('https://pub.example.com/g/prj_x')

    expect(iframe(container).getAttribute('title')).toBe('发布页')
  })
})

/**
 * spec 里的量级：超过 10 秒仍未 `load` 就撤掉遮罩（见
 * docs/design/galaxy/publication.md 的"主站壳"）。这里照着 spec 写字面量而不是引
 * 组件的常量——改了那个量级而没改 spec，这条就该红。
 */
const SPEC_COVER_TIMEOUT_MS = 10_000

/** 让这一帧"加载完了"。jsdom 不真去取那条地址，因此事件由测试自己发。 */
async function fireLoad(container: HTMLElement): Promise<void> {
  await act(async () => {
    iframe(container).dispatchEvent(new Event('load'))
  })
}

describe('加载态', () => {
  // 内容还没到的那一段不能是一片空白：访客得知道是在加载，不是坏了。
  it('load 之前有加载态，load 之后撤掉', async () => {
    const container = await renderFrame('https://pub.example.com/g/prj_x')

    expect(container.textContent, '内容还没到时什么都没有').toContain('正在加载内容')

    await fireLoad(container)

    expect(container.textContent).not.toContain('正在加载内容')
    // 加载完成后不留任何痕迹：正常的那条路上不能多出一层东西。
    expect(container.textContent).not.toContain('内容还在加载')
  })

  // **遮罩只铺到内容开始出现为止。** `load` 要等文档连同全部子资源都取完，而正文通常
  // 早就渲染出来了；再盖着不透明的遮罩，挡住的恰恰是已经能看的内容。
  it('到点仍未 load 时撤掉遮罩，只留一条可重试的状态条', async () => {
    vi.useFakeTimers()
    try {
      const container = await renderFrame('https://pub.example.com/g/prj_x')

      // 到点之前仍然是遮罩——这一档不是"等一会儿就撤"。
      await act(async () => {
        await vi.advanceTimersByTimeAsync(SPEC_COVER_TIMEOUT_MS - 1)
      })
      expect(container.textContent, '还没到点就把遮罩撤了').toContain('正在加载内容')

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1)
      })

      expect(container.textContent, '到点后遮罩还盖着').not.toContain('正在加载内容')
      expect(container.textContent).toContain('内容还在加载')
      // antd 在按钮文字的汉字之间插空格，因此按去空格后的文本比。
      expect(container.textContent?.replace(/\s/g, '')).toContain('重试')
      // 状态条**不遮住这一帧**：正文照旧可见、可交互。
      expect(container.querySelector('iframe'), '状态条把这一帧换掉了').not.toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  // 重试是把**这一帧**重新取一次，而不是重新解析地址——地址没变，没到的是内容。
  // 地址不变时只改 `src` 浏览器不会动，因此这里以"帧被重挂、加载态重新开始"为判据。
  it('状态条上的重试让这一帧重新取一次', async () => {
    vi.useFakeTimers()
    try {
      const container = await renderFrame('https://pub.example.com/g/prj_x')
      const before = iframe(container)

      await act(async () => {
        await vi.advanceTimersByTimeAsync(SPEC_COVER_TIMEOUT_MS)
      })

      await act(async () => {
        container.querySelector('button')?.click()
      })

      expect(iframe(container), '重试没有重新取这一帧').not.toBe(before)
      expect(iframe(container).getAttribute('src')).toBe('https://pub.example.com/g/prj_x')
      // 重新等起：遮罩回来了，而不是停在"还在加载"那条状态条上。
      expect(container.textContent).toContain('正在加载内容')
    } finally {
      vi.useRealTimers()
    }
  })

  // 换一条地址（工作台刷新预览）也要从头等起：上一份内容的加载态不属于新地址。
  it('换一条地址后重新进入加载态', async () => {
    const container = await renderFrame('https://pub.example.com/g/p/tok1/prj_x/')
    await fireLoad(container)
    expect(container.textContent).not.toContain('正在加载内容')

    await rerenderFrame('https://pub.example.com/g/p/tok2/prj_x/')

    expect(container.textContent).toContain('正在加载内容')
  })
})

/** 一条约定的图片预览消息。`source` 由调用方给：这一条通道的判据就是它。 */
function imagePreview(source: MessageEventSource | null): MessageEvent {
  return new MessageEvent('message', {
    data: {
      channel: FRAME_CHANNEL,
      version: FRAME_CHANNEL_VERSION,
      type: 'image-preview',
      payload: {
        images: [
          { src: 'https://pub.example.com/a.png', alt: '甲' },
          { src: 'https://bucket.example.com/b.png', alt: '乙' },
        ],
        index: 1,
      },
    },
    source,
  })
}

/** 灯箱里有没有出现这张图：预览渲染在宿主页面上，因此在 document.body 里找。 */
function lightboxShows(src: string): boolean {
  return document.body.querySelector(`img[src="${src}"]`) !== null
}

describe('接入桥（文档页 → 宿主）', () => {
  // 被点的那一张由宿主画到自己的页面上——这正是"灯箱在宿主侧"的样子。
  it('文档页发来预览请求后，宿主打开灯箱并显示被点的那一张', async () => {
    const container = await renderFrame('https://pub.example.com/g/prj_x/docs/index.html')

    await act(async () => {
      window.dispatchEvent(imagePreview(iframe(container).contentWindow))
    })

    expect(lightboxShows('https://bucket.example.com/b.png')).toBe(true)
  })

  // **起点由宿主给，且图仍按文档顺序排。** 这一条钉住的是"被点的那张是第 2 张"：
  // 如果改成把被点的图轮转到队首（受控下标写不回去时的绕法），上一张会在打开时
  // 就是灰的——明明还有第 1 张可看。
  it('从被点的那一张开起，且上一张仍可翻回去', async () => {
    const container = await renderFrame('https://pub.example.com/g/prj_x/docs/index.html')

    await act(async () => {
      window.dispatchEvent(imagePreview(iframe(container).contentWindow))
    })

    const progress = document.querySelector('.ant-image-preview-progress')
    expect(progress?.textContent, '计数不是文档顺序里的位置').toBe('2 / 2')

    const prev = document.querySelector('.ant-image-preview-switch-prev')
    expect(prev, '没有上一张按钮').not.toBeNull()
    expect(prev?.className, '打开时上一张就是灰的').not.toContain('disabled')
  })

  // **预览地址刷新不该把已经打开的灯箱关掉。** 预览地址带的是**短时凭证**，每次重取
  // 都是一张新票、一个新字符串，而内容一个字没变——工作台在窗口重新获得焦点时就会
  // 重取一次。把灯箱的寿命挂在这条地址上，表现就是"点开的图闪一下就没了"。
  it('预览地址刷新后，已打开的灯箱不被关掉', async () => {
    const container = await renderFrame('https://pub.example.com/g/p/tok1/prj_x/docs/')

    await act(async () => {
      window.dispatchEvent(imagePreview(iframe(container).contentWindow))
    })
    expect(lightboxShows('https://bucket.example.com/b.png')).toBe(true)

    await rerenderFrame('https://pub.example.com/g/p/tok2/prj_x/docs/')

    expect(lightboxShows('https://bucket.example.com/b.png'), '刷新预览把灯箱关掉了').toBe(true)
  })

  // **只认自己那一帧。** 不透明源的 origin 是 `null`，认不出是谁发的，
  // 因此判据只能是帧本身（见 SandboxFrame.tsx）。
  it('来自别处的同类消息被丢弃', async () => {
    await renderFrame('https://pub.example.com/g/prj_x/docs/index.html')

    await act(async () => {
      window.dispatchEvent(imagePreview(window))
    })

    expect(lightboxShows('https://bucket.example.com/b.png')).toBe(false)
  })

  // 信封之外一律忽略：类型不认识就当没收到。
  it('认不出类型的消息不产生任何效果', async () => {
    const container = await renderFrame('https://pub.example.com/g/prj_x/docs/index.html')
    const event = imagePreview(iframe(container).contentWindow)

    await act(async () => {
      window.dispatchEvent(
        new MessageEvent('message', {
          data: { ...(event.data as object), type: 'something-else' },
          source: iframe(container).contentWindow,
        }),
      )
    })

    expect(lightboxShows('https://bucket.example.com/b.png')).toBe(false)
  })
})
