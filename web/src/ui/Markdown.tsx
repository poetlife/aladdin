import { Typography, theme } from 'antd'
import { isValidElement, useMemo } from 'react'
import type { ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import type { Components } from 'react-markdown'
import { Link } from 'react-router-dom'
import remarkGfm from 'remark-gfm'

import { CommandBlock } from './CommandBlock'

/**
 * 把一段 Markdown 渲染成界面上的样子。
 *
 * 站内文档区的正文是 Markdown 源（`pages/docs/chapters/*.md`），页面这一侧只负责
 * "长什么样"——换主题、改间距都在这里一处生效，章节源里不含任何样式
 * （见 docs/design/web/agent-readable.md）。
 *
 * 元素映射刻意只覆盖文档里真用得到的那几种：多映射一种就多一处要维护的呈现，
 * 而正文里出现没用过的元素时，默认渲染（浏览器原始样式）反而更容易被发现。
 *
 * 颜色与间距全部取自 `theme.useToken()`，不写死色值——这是"文档与外壳同源"的
 * 落点：换主题时它跟着换（见 docs/design/web/docs-area.md）。
 */
export function Markdown({ children }: { children: string }): ReactNode {
  const { token } = theme.useToken()

  const components = useMemo<Components>(
    () => ({
      h1: ({ children: content }) => (
        <Typography.Title level={3} style={{ marginTop: 0 }}>
          {content}
        </Typography.Title>
      ),
      h2: ({ children: content }) => (
        <Typography.Title level={4} style={{ marginTop: token.marginMD }}>
          {content}
        </Typography.Title>
      ),
      h3: ({ children: content }) => (
        <Typography.Title level={5} style={{ marginTop: token.marginMD }}>
          {content}
        </Typography.Title>
      ),
      p: ({ children: content }) => (
        <Typography.Paragraph style={{ marginBottom: token.marginSM }}>
          {content}
        </Typography.Paragraph>
      ),
      ul: ({ children: content }) => (
        <ul style={{ margin: `0 0 ${token.marginSM}px`, paddingInlineStart: token.paddingLG }}>
          {content}
        </ul>
      ),
      ol: ({ children: content }) => (
        <ol style={{ margin: `0 0 ${token.marginSM}px`, paddingInlineStart: token.paddingLG }}>
          {content}
        </ol>
      ),
      li: ({ children: content }) => (
        <li style={{ marginBottom: token.marginXXS }}>{content}</li>
      ),
      // 行内代码。围栏代码块走下面那条：它整块交给 CommandBlock，不走这里。
      code: ({ children: content }) => <Typography.Text code>{content}</Typography.Text>,
      // 围栏代码块。取的是它内部的纯文本——命令要能整段复制，带上高亮或折行标记
      // 都会让复制出来的东西不是一条能跑的命令。
      pre: ({ children: content }) => <CommandBlock>{textOf(content)}</CommandBlock>,
      a: ({ href, children: content }) =>
        href !== undefined && href.startsWith('/') ? (
          // 站内链接走路由：不然点一次就是一次整页刷新，外壳也跟着重来一遍。
          <Link to={href}>{content}</Link>
        ) : (
          <a href={href} target="_blank" rel="noreferrer">
            {content}
          </a>
        ),
      blockquote: ({ children: content }) => (
        <div
          style={{
            margin: `0 0 ${token.marginSM}px`,
            paddingInlineStart: token.paddingSM,
            borderInlineStart: `${token.lineWidthBold}px solid ${token.colorBorderSecondary}`,
            color: token.colorTextSecondary,
          }}
        >
          {content}
        </div>
      ),
      // 表格自带横向滚动：窄屏下按原宽排会把内容挤出视口，
      // 而这一区的表格是**取值参考**，被截掉的那几列正是要查的东西。
      table: ({ children: content }) => (
        <div style={{ overflowX: 'auto', marginBottom: token.marginSM }}>
          <table
            style={{
              borderCollapse: 'collapse',
              width: '100%',
              fontSize: token.fontSizeSM,
            }}
          >
            {content}
          </table>
        </div>
      ),
      th: ({ children: content }) => (
        <th
          style={{
            textAlign: 'start',
            padding: `${token.paddingXS}px ${token.paddingSM}px`,
            border: `${token.lineWidth}px solid ${token.colorBorderSecondary}`,
            background: token.colorFillTertiary,
            whiteSpace: 'nowrap',
          }}
        >
          {content}
        </th>
      ),
      td: ({ children: content }) => (
        <td
          style={{
            padding: `${token.paddingXS}px ${token.paddingSM}px`,
            border: `${token.lineWidth}px solid ${token.colorBorderSecondary}`,
            verticalAlign: 'top',
          }}
        >
          {content}
        </td>
      ),
      hr: () => (
        <hr
          style={{
            margin: `${token.marginMD}px 0`,
            border: 0,
            borderTop: `${token.lineWidth}px solid ${token.colorBorderSecondary}`,
          }}
        />
      ),
      // 等宽字体只在这两处补：antd 不给裸元素定字体，正文里出现命令名与取值时
      // 用系统默认比例字体，`.md` 里是代码、页面上却看不出来。
      strong: ({ children: content }) => (
        <strong style={{ fontWeight: token.fontWeightStrong }}>{content}</strong>
      ),
    }),
    [token],
  )

  return (
    // 表格要能用 GFM 那种写法（正文里就是 `| 字段 | 取值 |`），因此显式开它。
    <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
      {children}
    </ReactMarkdown>
  )
}

/**
 * 取出一棵 React 子树里的纯文本。
 *
 * 只用于围栏代码块：它整块是数据，不是标记，因此把里面的节点摊平成字符串，
 * 交给 `CommandBlock` 去渲染与复制。
 */
function textOf(node: ReactNode): string {
  if (typeof node === 'string') return node
  if (typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map((child) => textOf(child)).join('')
  if (isValidElement<{ children?: ReactNode }>(node)) return textOf(node.props.children)
  return ''
}
