import { useState } from 'react'
import { Empty, Flex, Skeleton, Tag, Typography } from 'antd'
import { ChevronDown, ChevronRight } from 'lucide-react'

import type { Asset, FileEntry } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { MONOSPACE } from '../../theme'
import { AssetMedia } from './AssetMedia'

interface SourceViewProps {
  entries: readonly FileEntry[]
  /** 工程的资产清单，用来认出一份资产条目的类型与标题。取不到时只少这两样。 */
  assets: readonly Asset[]
  /** 当前选中的那一份的路径；空串表示还没选。 */
  selectedPath: string
  /** 选中那一份的原文（只有文本条目有）。 */
  sourceText: string
  sourceBusy: boolean
  onSelect: (entry: FileEntry) => void
}

/**
 * 源码视图：左栏一棵文件树，右栏是选中的那一份（见 docs/design/galaxy/authoring.md）。
 *
 * 它是**文件树 + 选中的那一份**，不是"一份正文"：内容本来就是一组具名文件，把它
 * 伪装成一份文本会立刻引出"我改的到底是哪一份"这个无法回答的问题。
 *
 * **只读**：网页端不改内容，写入只有命令行一条路，因此这里没有任何可编辑的控件。
 * 右侧按"这一份是什么"呈现——文本条目给原文，资产条目**就地渲染**（图片、视频、
 * 音频各用各的控件），因为对一张图片，"它写了什么"就是那张图片。
 */
export function SourceView({
  entries,
  assets,
  selectedPath,
  sourceText,
  sourceBusy,
  onSelect,
}: SourceViewProps): React.ReactNode {
  // 被**折叠起来**的目录。记折叠而不是记展开：新出现的目录因此天然是展开的，
  // 不需要一条"清单变了就同步一次展开集"的逻辑。
  const [collapsedDirs, setCollapsedDirs] = useState<string[]>([])

  const selectedEntry = entries.find((entry) => entry.path === selectedPath)
  const selectedAsset =
    selectedEntry?.source.case === 'assetId'
      ? assets.find((asset) => asset.id === selectedEntry.source.value)
      : undefined
  // 就地渲染一份资产要的三样。**地址取条目自己带的那一条**：列出草稿时服务端给
  // 每条都签发了短时地址，因此资产清单没取到（缺 `galaxy.asset.read`）时渲染仍然
  // 成立。类型与标题只有资产清单知道，取不到就不猜——只留一句"类型未知"。
  const assetURL =
    selectedEntry !== undefined && selectedEntry.source.case === 'assetId' ? selectedEntry.url : ''
  const assetLabel =
    selectedAsset?.title !== undefined && selectedAsset.title !== ''
      ? selectedAsset.title
      : (selectedEntry?.path ?? '')

  /** 折叠或展开左栏里的一支。 */
  function toggleDir(key: string): void {
    setCollapsedDirs((current) =>
      current.includes(key) ? current.filter((candidate) => candidate !== key) : [...current, key],
    )
  }

  /**
   * 左栏的一棵子树。
   *
   * 目录行只管折叠，不参与选中——它不是一个"可以看的东西"，点它只回答"这一支里
   * 有什么"。缩进按深度给，所以文件行上不必再写它的目录前缀。
   */
  function renderTree(nodes: readonly SourceTreeNode[], depth: number): React.ReactNode {
    const indent = 6 + depth * 12
    return nodes.map((node) => {
      const expanded = node.children.length > 0 && !collapsedDirs.includes(node.dirKey)
      return (
        <div key={node.dirKey}>
          {node.children.length > 0 && (
            <Flex
              align="center"
              gap={4}
              onClick={() => toggleDir(node.dirKey)}
              style={{
                cursor: 'pointer',
                padding: '4px 6px',
                paddingInlineStart: indent,
                borderRadius: 4,
              }}
            >
              {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
              <Typography.Text style={{ fontSize: 12 }} ellipsis>
                {node.name}/
              </Typography.Text>
            </Flex>
          )}
          {expanded && renderTree(node.children, depth + 1)}
          {node.entry !== undefined && renderEntry(node.entry, node.name, indent)}
        </div>
      )
    })
  }

  /** 左栏里的一份文件：点它看这一份是什么。 */
  function renderEntry(entry: FileEntry, name: string, indent: number): React.ReactNode {
    return (
      <Flex
        key={entry.path}
        align="center"
        justify="space-between"
        gap={4}
        onClick={() => onSelect(entry)}
        style={{
          cursor: 'pointer',
          padding: '4px 6px',
          paddingInlineStart: indent,
          borderRadius: 4,
          background: entry.path === selectedPath ? 'rgba(128,128,128,0.16)' : undefined,
        }}
      >
        <Typography.Text style={{ fontSize: 12 }} ellipsis>
          {name}
        </Typography.Text>
        {entry.source.case === 'assetId' && (
          <Tag style={{ marginInlineEnd: 0 }} color="blue">
            资产
          </Tag>
        )}
      </Flex>
    )
  }

  return (
    <Flex gap={8} style={{ height: '100%', minHeight: 0 }}>
      <div
        style={{
          width: 220,
          overflowY: 'auto',
          border: '1px solid rgba(128,128,128,0.25)',
          borderRadius: 6,
          padding: 4,
          flexShrink: 0,
        }}
      >
        {entries.length === 0 ? (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            草稿还是空的。用命令行 push 一组文件上来。
          </Typography.Text>
        ) : (
          renderTree(buildSourceTree(entries), 0)
        )}
      </div>
      <div
        style={{
          flex: 1,
          minWidth: 0,
          overflow: 'auto',
          border: '1px solid rgba(128,128,128,0.25)',
          borderRadius: 6,
          padding: 8,
        }}
      >
        {selectedEntry === undefined ? (
          <Empty description="选一份文件看它写了什么" image={Empty.PRESENTED_IMAGE_SIMPLE} />
        ) : selectedEntry.source.case === 'assetId' ? (
          // 二进制不进来当文本读，但**照它本来的样子显示**："能渲染"正是网页端比
          // 命令行多出来的那一样，退化成一句类型说明就等于在这个强项上缺席。
          <Flex vertical gap={8}>
            <Typography.Text type="secondary">
              这一份是资产（{selectedAsset?.mediaType ?? '类型未知'}）。
            </Typography.Text>
            {assetURL !== '' && (
              <Typography.Link href={assetURL} target="_blank" rel="noopener noreferrer">
                在新标签页打开
              </Typography.Link>
            )}
            <AssetMedia
              url={assetURL}
              mediaType={selectedAsset?.mediaType ?? ''}
              label={assetLabel}
            />
          </Flex>
        ) : sourceBusy ? (
          <Skeleton active paragraph={{ rows: 6 }} />
        ) : (
          <pre
            style={{
              margin: 0,
              fontFamily: MONOSPACE,
              fontSize: 13,
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-all',
            }}
          >
            {sourceText}
          </pre>
        )}
      </div>
    </Flex>
  )
}

/**
 * 左栏的一棵树：由路径段折出来。
 *
 * 中间节点是目录、叶子是文件。**同一层上目录与同名文件可以并存**（`img` 与
 * `img/x.png`），因此一个节点的两个身份各渲染各的一行，谁也不会把谁挤掉。
 */
interface SourceTreeNode {
  /** 这一层的名字——路径的最后一段。 */
  name: string
  /** 目录的完整前缀（形如 `img/`）；它也是折叠状态记的键。 */
  dirKey: string
  /** 这一层上有没有一份同名文件。 */
  entry?: FileEntry | undefined
  children: SourceTreeNode[]
}

/**
 * 把一组路径折成树。
 *
 * 路径本来就是树形的：平铺列表把 `img/` 这样的前缀在每一条上重写一遍，一次折叠
 * 就能省掉，而树没有多出任何信息。顺序按清单原序，因此与 push 上来的顺序一致。
 */
function buildSourceTree(entries: readonly FileEntry[]): SourceTreeNode[] {
  const roots: SourceTreeNode[] = []
  for (const entry of entries) {
    const segments = entry.path.split('/')
    let level = roots
    let prefix = ''
    segments.forEach((segment, index) => {
      prefix = prefix === '' ? segment : `${prefix}/${segment}`
      let node = level.find((candidate) => candidate.name === segment)
      if (node === undefined) {
        node = { name: segment, dirKey: prefix, children: [] }
        level.push(node)
      }
      if (index === segments.length - 1) {
        node.entry = entry
      }
      level = node.children
    })
  }
  return roots
}
