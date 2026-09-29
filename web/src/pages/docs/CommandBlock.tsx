import { Typography, theme } from 'antd'

import { MONOSPACE } from '../../theme'

/**
 * 等宽命令块。它只做"命令要能整段复制"这一件事。
 *
 * **它被两个章节共用**（命令行、创作与发布）：当初只有一处调用时留在页面里，
 * 出现第二个使用方后抽到本文件——两处各写一份的话，复制入口或折行行为某天
 * 只改了一处，就会表现为"这一章的命令复制不全"。
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
