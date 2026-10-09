import { Card, Space, Typography, theme } from 'antd'
import { BookOpen, ChevronRight, FileJson } from 'lucide-react'
import { Link } from 'react-router-dom'

import { CHAPTERS } from './chapters/manifest'

/**
 * 文档区的索引页。
 *
 * 它是**公开**的：准入见 routes.tsx 里那条文档分支——未认证也能读。零权限的主体
 * 最需要它，否则"先装命令行才能登录、登录了才看得到怎么装命令行"这个环闭不上
 * （见 docs/design/web/docs-area.md）。
 *
 * 章节清单**不在这里**：它由 `chapters/manifest.tsx` 派生，与本页、路由表、
 * 送给 agent 的 `.md` 和 `llms.txt` 是同一份。加一章因此只改那一处。
 *
 * 但它并非只有"章节"一种东西：接口参考是一份**生成出来的静态页**
 * （见 docs/design/api-docs/README.md），这里只放一条指向它的链接。两者的
 * 区别见下面两个入口各自的注释。
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
          {CHAPTERS.map((chapter) => (
            <Link key={chapter.slug} to={`/docs/${chapter.slug}`}>
              <EntryRow icon={chapter.icon} title={chapter.title} blurb={chapter.summary} />
            </Link>
          ))}

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
