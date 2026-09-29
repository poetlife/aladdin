import { Card, Space, Typography, theme } from 'antd'
import { BookOpen, ChevronRight, FileJson, Sparkles, Terminal } from 'lucide-react'
import { Link } from 'react-router-dom'

/**
 * 文档区的索引页。
 *
 * 文档是**前端的一部分**（路由 + 组件），不是另一个站点：它用同一套外壳、
 * 同一套主题，将来"放出去"改变的是准入，而不是换宿主
 * （见 docs/design/web/docs-area.md）。
 *
 * 但它并非只有"章节"一种东西：接口参考是一份**生成出来的静态页**
 * （见 docs/design/api-docs/README.md），这里只放一条指向它的链接。两者的
 * 区别见下面两个入口各自的注释。
 *
 * 章节清单在这里手写，与 router.tsx 里的子路由一一对应——**加一章要同时改
 * 两处**。只有一章时不为它抽一层清单：两份定义比一处手写更容易漂，等章节
 * 真的多起来再抽。
 */
export function DocsIndexPage(): React.ReactNode {
  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <BookOpen size={16} />
            文档
          </Space>
        }
      >
        <Space orientation="vertical" size="small" style={{ width: '100%' }}>
          <Link to="/docs/cli">
            <EntryRow
              icon={<Terminal size={18} />}
              title="命令行"
              blurb="怎么装、怎么登录、怎么升级"
            />
          </Link>

          <Link to="/docs/galaxy">
            <EntryRow
              icon={<Sparkles size={18} />}
              title="创作与发布"
              blurb="写一份 HTML，用记号引用素材，发布成别人能打开的页面"
            />
          </Link>

          {/* 接口参考**不是一章**，而是一条外链：它是 proto 的派生物，由
              `make api-docs` 生成、由静态目录直接服务（见
              docs/design/api-docs/README.md），不随主题走、也不需要进路由。

              必须是普通 <a> 而不是 <Link>：它不在路由表里，走 <Link> 会让
              React Router 接住这次跳转、匹配不到路由、落到 `*` 那条兜底上，
              表现是"点了接口文档却回到了首页"——而且没有任何报错。

              相对路径也是有意的：绝对路径会把部署实例的域名写进仓库
              （见 docs/deploy.md 的可验证性表）。 */}
          <a href="/api-docs/">
            <EntryRow
              icon={<FileJson size={18} />}
              title="接口参考"
              blurb="每个 RPC 方法的路径与鉴权要求（公开免登录）"
            />
          </a>
        </Space>
      </Card>
    </Space>
  )
}

/** 索引里的一行。它只管长相，"跳到哪、怎么跳"由外面那层包裹元素决定。 */
function EntryRow({
  icon,
  title,
  blurb,
}: {
  icon: React.ReactNode
  title: string
  blurb: string
}): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: token.paddingSM,
        border: `1px solid ${token.colorBorderSecondary}`,
        borderRadius: token.borderRadius,
      }}
    >
      <span style={{ color: token.colorPrimary, display: 'flex', flexShrink: 0 }}>{icon}</span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div>{title}</div>
        <Typography.Text type="secondary">{blurb}</Typography.Text>
      </div>
      <ChevronRight size={16} style={{ flexShrink: 0, opacity: 0.6 }} />
    </div>
  )
}
