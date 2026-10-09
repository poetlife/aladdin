import { Typography, theme } from 'antd'

import { MONOSPACE } from '../theme'

/**
 * 等宽命令块。它只做"命令要能整段复制"这一件事。
 *
 * 它被文档区的每一章共用：正文是 Markdown，围栏代码块由 `Markdown` 统一渲染成
 * 它——因此"复制入口有没有、折行怎么折"在一个地方定，而不是每章各写一遍。
 *
 * 复制入口用 antd 的 `Typography` 自带的那一个（仓库里已有九处同样的用法），
 * 不自己碰剪贴板：手动选中一段命令容易漏掉续行，也容易把行首的换行带进去。
 */
export function CommandBlock({ children }: { children: string }): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <div style={{ position: 'relative' }}>
      <pre
        style={{
          margin: 0,
          padding: token.paddingSM,
          // 右上角留给复制入口：正文不钻到它下面去。
          paddingRight: token.paddingSM + token.padding,
          background: token.colorFillTertiary,
          borderRadius: token.borderRadius,
          fontFamily: MONOSPACE,
          fontSize: 13,
          // 命令普遍很长，窄屏下尤其。**折行而不是横向滚动**：横向滚动会把
          // 后半截藏起来，而这里藏的是 URL——看着像命令就长这样。
          whiteSpace: 'pre-wrap',
          overflowWrap: 'anywhere',
        }}
      >
        {children}
      </pre>
      {/* 没有子节点，因此渲染出来的就是这个复制图标本身。 */}
      <Typography.Text
        copyable={{ text: children }}
        style={{ position: 'absolute', top: token.paddingXS, right: token.paddingXS }}
      />
    </div>
  )
}
