import { create } from '@bufbuild/protobuf'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'

import {
  ContentSlot,
  type Project,
  ProjectSchema,
  type Version,
  VersionSchema,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { WorkbenchTopBar } from './WorkbenchTopBar'

// React 19 要求显式声明这是 act 环境，否则每次 render 都会打印警告。
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

/** 长到在窄屏那一行里必然放不下的工程名：issue #71 里被主操作盖住的就是这一档。 */
const LONG_NAME = '权钱交易所 · 一名省委书记的沉浮与这座城市的十年（修订版）'

function projectNamed(name: string, published: boolean): Project {
  return create(ProjectSchema, {
    id: 'p1',
    name,
    slots: [{ slot: ContentSlot.SITE, published, publishedUrl: 'https://pub.example.com/a' }],
  })
}

function version(): Version {
  return create(VersionSchema, {
    id: 'v1',
    seq: 1n,
    savedAt: '2026-10-08T12:00:00Z',
    slot: ContentSlot.SITE,
  })
}

interface RenderOptions {
  narrow: boolean
  name?: string
  versions?: readonly Version[]
  /** 这个槽已经发布出去过：发布那颗主操作因此叫「更新发布」。 */
  published?: boolean
}

/** 单独挂起顶栏：它这一排的形状就是这一组要验的东西，不必拉起整页。 */
async function renderTopBar({
  narrow,
  name = LONG_NAME,
  versions = [version()],
  published = false,
}: RenderOptions): Promise<HTMLElement> {
  const project = projectNamed(name, published)
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root?.render(
      <MemoryRouter>
        <WorkbenchTopBar
          project={project}
          narrow={narrow}
          versions={versions}
          slot={project.slots[0]}
          slots={project.slots}
          slotBusy={false}
          onSwitchSlot={() => {}}
          onAddSlot={() => {}}
          canWrite
          canPublish
          publishEnabled
          contentEnabled
          assetPanelEnabled
          versionBusy={false}
          publishBusy={false}
          draftHasProblems={false}
          draftEntries={[]}
          onOpenAssets={() => {}}
          onOpenVersions={() => {}}
          onSaveVersion={() => {}}
          onPublish={() => {}}
          onProjectChange={() => {}}
        />
      </MemoryRouter>,
    )
  })
  return container
}

/** 按 `aria-label` 找一颗按钮：窄屏下这些控件只剩图标，文本定位不到它们。 */
function buttonByLabel(container: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(container.querySelectorAll('button')).find(
    (candidate) => candidate.getAttribute('aria-label') === label,
  )
}

afterEach(async () => {
  await act(async () => {
    root?.unmount()
  })
  root = null
  // 气泡挂在 body 上，不换掉就会漏到下一条用例里。
  document.body.replaceChildren()
})

describe('顶栏窄屏的一行', () => {
  // **让出来的是标题。** 这一条曾经反过来：标题按钮默认不换行，两个能让它被压到
  // 内容宽度以下的 `minWidth: 0`（按钮一处、里面那段文字一处）谁都没写，于是长标题
  // 按原宽度溢出、压到右边的按钮底下。jsdom 不做排版，因此验的是"能不能被压窄"：
  // 少任意一条，溢出就回来了。
  it('长标题可以被压窄并截断，右侧的按钮不参与压缩', async () => {
    const container = await renderTopBar({ narrow: true })

    const title = buttonByLabel(container, '工程信息')
    expect(title, '标题不在这一行上').not.toBeUndefined()
    expect(title?.style.minWidth, '标题这一颗压不下去').toBe('0px')

    const titleText = title?.querySelector<HTMLElement>('.ant-typography')
    expect(titleText?.style.overflow, '截断的那三条缺一条都会溢出').toBe('hidden')
    expect(titleText?.style.textOverflow).toBe('ellipsis')
    expect(titleText?.style.whiteSpace).toBe('nowrap')
    expect(titleText?.style.minWidth).toBe('0px')
    // DOM 里仍是完整的名字：截的是它在那一行的显示，不是这个名字。
    expect(titleText?.textContent).toBe(LONG_NAME)

    // 右边那一组不跟着压：主操作与「更多」是这一排的下手处，压它们等于把点击区域匝起来。
    const actions = title?.parentElement?.nextElementSibling as HTMLElement | undefined
    expect(actions?.style.flexShrink).toBe('0')
    expect(buttonByLabel(container, '更多')).not.toBeUndefined()
  })

  // 常驻的那一颗主操作在窄屏只剩图标，名字靠 aria-label 保住——与返回、状态条的撤回、
  // 预览工具条的「单独打开」同一条做法（见 docs/design/web/responsive.md 的「工作台窄屏」）。
  // 两个状态下都是同一颗按钮，只是名字不同：还没发布过叫「发布」，发过一次叫「更新发布」。
  it.each([
    ['发布', false] as const,
    ['更新发布', true] as const,
  ])('主操作只剩图标，名字仍是「%s」', async (label, published) => {
    const container = await renderTopBar({ narrow: true, published })

    const publish = buttonByLabel(container, label)
    expect(publish, `主操作「${label}」不在这一行上`).not.toBeUndefined()
    expect(publish?.textContent, '窄屏主操作不该再占一段文字').toBe('')
    // 下拉箭头留着：它说明这一下开的是菜单，不是直接发布。
    expect(publish?.querySelector('svg')).not.toBeNull()
  })

  // 缺可发布版本时主操作换成存版本，窄屏同样只剩图标——两个状态都得让出宽度。
  it('主操作是存版本时也只剩图标', async () => {
    const container = await renderTopBar({ narrow: true, versions: [] })

    const save = buttonByLabel(container, '存为版本')
    expect(save, '存版本不在这一行上').not.toBeUndefined()
    expect(save?.textContent).toBe('')
  })

  it('标题被截断了，完整名称点开工程信息弹层仍在', async () => {
    const container = await renderTopBar({ narrow: true })

    const title = buttonByLabel(container, '工程信息')
    await act(async () => {
      title?.click()
    })

    const nameInput = Array.from(document.body.querySelectorAll('input')).find(
      (candidate) => candidate.value === LONG_NAME,
    )
    expect(nameInput, '弹层里看不到完整名称').not.toBeUndefined()
  })

  // 非目标：宽屏顶栏不动。那里没有东西跟标题抢宽度，多一条省略号只会让短标题也可疑。
  it('宽屏不截断，主操作仍是带字的那一颗', async () => {
    const container = await renderTopBar({ narrow: false })

    const title = buttonByLabel(container, '工程信息')
    expect(title?.style.minWidth, '宽屏不该设最小宽度').toBe('')
    const titleText = title?.querySelector<HTMLElement>('.ant-typography')
    expect(titleText?.style.textOverflow, '宽屏不该截断').toBe('')
    expect(titleText?.style.overflow).toBe('')

    const publish = buttonByLabel(container, '发布')
    expect(publish, '宽屏主操作没有名字了').toBeUndefined()
    const namedPublish = Array.from(container.querySelectorAll('button')).find(
      (candidate) => candidate.textContent?.includes('发布') === true,
    )
    expect(namedPublish, '宽屏主操作不在这一排上').not.toBeUndefined()
  })
})
