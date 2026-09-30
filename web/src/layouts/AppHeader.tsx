import { AutoComplete, Button, Layout, theme } from 'antd'
import { Building2 } from 'lucide-react'
import { useEffect, useState } from 'react'

import { useSession } from '../auth'
import { formatScope, parseScope, useMyScopes, useScopes } from '../rbac'
import { ThemeSwitch } from '../theme'

const { Header } = Layout

interface AppHeaderProps {
  /** 开关的图标。宽屏是收放导轨，窄屏是开抽屉——语义由外壳定，这里只管画。 */
  toggleIcon: React.ReactNode
  /** 开关的无障碍标签，同样由外壳给出。 */
  toggleLabel: string
  onToggle: () => void
}

/**
 * 页头：与**当前视图**相关的控件——折叠开关、管理范围、主题。
 *
 * 账号入口不在这里，在侧边栏底部（见 AppSidebar.tsx）：它回答的是"我是谁"，
 * 与导航同类，因此与导航同处一侧。
 *
 * 那颗开关在宽屏与窄屏下做的事不同（收放导轨 / 开抽屉），页头不自己判断是哪一种，
 * 图标、标签与行为都由外壳注入——判断"是否窄屏"的地方只有一处。
 *
 * 中间那个控件叫**管理范围**：它回答"我在哪个授权范围下工作"。这个词是刻意的，
 * 它既不叫"作用域"（会与权限码的"领域"段和凭证默认作用域混起来），也不该被
 * 理解成业务域/产品线。三个概念的区分见 docs/design/rbac/management-ui.md。
 *
 * 背景与分隔线取自 `theme.useToken()`，而不是 antd 的 `Layout.Header` 默认值——
 * 后者是硬编码的深色 `#001529`，不随明暗算法变化，会在亮色主题下留一条深色带。
 */
export function AppHeader({ toggleIcon, toggleLabel, onToggle }: AppHeaderProps): React.ReactNode {
  const { token } = theme.useToken()
  const { scope, setScope } = useSession()

  // 候选项首选**已登记的范围目录**（这个部署里有哪些范围，见
  // docs/design/rbac/scopes.md），读不到时退回**我自己的绑定范围**——后者是
  // 前者的子集，退回不丢候选。目录为空既可能是没权限，也可能是还没登记过，
  // 两种情形下退回都一样对。
  const catalog = useScopes()
  const mine = useMyScopes()

  // 输入框里显示的永远是**界面写法**：空串显示成「全局」。
  const [draft, setDraft] = useState(() => formatScope(scope))

  // 范围可能在别处变化（首次登录时服务端会采纳凭证的默认范围），
  // 草稿要跟着回到与它一致的那一份，否则输入框会停在一个已经作废的值上。
  useEffect(() => {
    setDraft(formatScope(scope))
  }, [scope])

  async function commit(input: string): Promise<void> {
    const next = parseScope(input)
    // 先归一化显示：输入「全局」或留空之后，框里回的应该是同一个写法。
    setDraft(formatScope(next))
    if (next !== scope) {
      await setScope(next)
    }
  }

  const candidates =
    catalog.scopes.length > 0 ? catalog.scopes.map((s) => s.path) : mine.scopes

  // 不额外塞一个「全局」：请求里的空范围在服务端表示"不指定、按凭证默认范围解析"，
  // 不是"我要全局"，因此它不是一个用户能点选的落点——真正的全局会作为**解析结果**
  // 出现在这个框里。
  const options = candidates.map((s) => ({ value: formatScope(s), label: formatScope(s) }))

  return (
    <Header
      style={{
        // 页头默认是等高单行（height / line-height 都取自 headerHeight），
        // 窄屏下这一行装不下工具区。改成等高不固定、允许换行，
        // 桌面端靠 minHeight 与侧边栏的品牌区齐平，窄屏则多占一行而不是把控件挤出去。
        height: 'auto',
        minHeight: token.controlHeight * 2,
        lineHeight: 1.5,
        padding: '8px 16px',
        display: 'flex',
        alignItems: 'center',
        flexWrap: 'wrap',
        gap: 12,
        background: token.colorBgContainer,
        borderBottom: `1px solid ${token.colorBorderSecondary}`,
      }}
    >
      <Button type="text" aria-label={toggleLabel} icon={toggleIcon} onClick={onToggle} />

      {/* 工具区自己也是一个可换行的 flex 容器，而不是一个整体：
          Space 是一整个 flex 项，窄屏下它会整体溢出到页头之外（控件被裁掉），
          换成容器后每个控件各自找位置，只会多占一行。

          它**自己撑满页头剩下的宽度**，而不是靠 `marginLeft: auto` 靠到右边：
          用 auto 边距时容器宽度由内容决定，而范围框的 flex-basis 是 160px、
          内容的最大宽度只有 128px——容器据此算出的"最大内容宽度"比它内部
          换行所需的小，于是换行判定认为主题开关放不下，把它挪到第二行，
          页头中间却还空着一大片。撑满之后宽度是确定的，这类误判不再发生。 */}
      <div
        style={{
          flex: '1 1 auto',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'flex-end',
          flexWrap: 'wrap',
          gap: 12,
          minWidth: 0,
        }}
      >
        {/* 宽度是弹性的：桌面端封顶 260，手机上有多少用多少。
            写死 260 会在 360px 的屏上把主题切换挤出页头。
            这个控件是"可手输 + 有建议"，不是只读选择：候选项列不出来时
            （没有读主体的权限、主体还没登记、这次读取失败）仍然要能直接写路径，
            空态里说明这一点。控件不挂解释——它讲给谁听、讲什么见
            docs/design/rbac/management-ui.md 的"顶栏：管理范围"。 */}
        <AutoComplete
          value={draft}
          options={options}
          onChange={(value) => setDraft(value)}
          // 选中一个候选项是一次明确的决定，立即生效；手输则等回车或失焦再提交。
          onSelect={(value) => void commit(value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') void commit(draft)
          }}
          onBlur={() => void commit(draft)}
          placeholder="管理范围"
          prefix={<Building2 size={14} />}
          // 列不出候选是可预期的一种状态（没权限、主体未登记、这次读取失败）。
          // 这句放在下拉空态里：想看候选的人正好在这里读到"可以手输"。
          notFoundContent="暂时列不出可用的范围，可直接输入路径。"
          style={{ flex: '1 1 160px', maxWidth: 260, minWidth: 0 }}
          aria-label="管理范围"
        />

        <ThemeSwitch />
      </div>
    </Header>
  )
}
