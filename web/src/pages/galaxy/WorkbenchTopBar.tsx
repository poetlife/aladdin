import { Button, Dropdown, Flex, Space } from 'antd'
import type { MenuProps } from 'antd'
import { ArrowLeft, ChevronDown, History, Images, Layers, Rocket } from 'lucide-react'
import { useNavigate } from 'react-router-dom'

import type { FileEntry, Project, Version } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { track } from '../../telemetry/track'
import { formatTime } from './format-time'
import { ProjectInfoPopover } from './ProjectInfoPopover'

interface WorkbenchTopBarProps {
  project: Project
  versions: readonly Version[]
  /** 是否持有写权限（galaxy.project.write）。 */
  canWrite: boolean
  /** 是否持有发布权限（galaxy.project.publish）。 */
  canPublish: boolean
  /** 是否启用发布（capabilities.publishEnabled）。为假时不渲染任何发布入口。 */
  publishEnabled: boolean
  /**
   * 这个部署有没有配置对象存储。**它是内容的前提**：没配桶时字节没有地方放，
   * 草稿、版本与产物整体不可用——那些入口因此不渲染，而不是渲染一个点了报错的
   * 控件（见 docs/design/galaxy/asset-library.md 的"未配置时"）。
   */
  contentEnabled: boolean
  /** 资产面板是否可以打开（能力启用且持有读权限）。 */
  assetPanelEnabled: boolean
  versionBusy: boolean
  publishBusy: boolean
  /**
   * 当前草稿的校验结论是「有问题」。
   *
   * 发布读的是版本，不是草稿。但与这份草稿清单相同的版本会被同一套规则拒绝，
   * 顶栏不再提供那一次点击。清单不同的历史版本不受这一条影响。
   */
  draftHasProblems: boolean
  /** 当前草稿清单，用来判断哪个版本会得到同一份拒绝。 */
  draftEntries: readonly FileEntry[]
  onOpenAssets: () => void
  onOpenVersions: () => void
  onSaveVersion: () => void
  onPublish: (versionId: string) => void
  onProjectChange: (project: Project) => void
}

/**
 * 工作台顶栏：去哪里、有哪些集合可以打开、以及推进流程的两个动作。
 *
 * 资产与版本是"一批东西"，从这里的两个入口以弹层打开（见
 * docs/design/galaxy/authoring.md 的"主区铺满，集合进弹层"）。
 *
 * **这一排的形态统一：描边的都是"可以做的事"，实心的只有主操作。** 把打开集合的两个
 * 入口画成无底色的文字按钮，它们在一排按钮里就成了没有下手处的标签——同一排里出现
 * 两种待遇，人得先分辨"哪个能点"才能动手。
 *
 * **推进流程的动作在这里各只有一处**：发布与更新发布（决定哪一版发出去）在右端；
 * 撤回不在这里，它紧挨着状态条上那个要被作废的地址（那里是它的落点，不是第二个入口）。
 *
 * 主操作随流程位置变化：能发布时"发布"是主，"存为版本"降为次要；还不能发布时
 * 反过来。任何时刻只有一个 primary——"一页只有一个主操作"，只是"哪一个"
 * 由状态决定（见 docs/design/uiux/README.md）。
 *
 * 还没有版本时"发布"直接禁用。原因不另写提示：状态条已经写着还没有版本。
 * 草稿有问题时，与当前清单相同的版本同样禁用：状态条上的问题清单就是原因。
 */
export function WorkbenchTopBar({
  project,
  versions,
  canWrite,
  canPublish,
  publishEnabled,
  contentEnabled,
  assetPanelEnabled,
  versionBusy,
  publishBusy,
  draftHasProblems,
  draftEntries,
  onOpenAssets,
  onOpenVersions,
  onSaveVersion,
  onPublish,
  onProjectChange,
}: WorkbenchTopBarProps): React.ReactNode {
  const navigate = useNavigate()

  const draftSignature = manifestSignature(draftEntries)
  const versionBlocked = (version: Version): boolean =>
    draftHasProblems && manifestSignature(version.entries) === draftSignature
  const publishableCount = versions.filter((version) => !versionBlocked(version)).length
  const publishAvailable = publishEnabled && canPublish && publishableCount > 0

  // 新的排在前面：要发布的多半是刚存的那一版。
  const publishItems: NonNullable<MenuProps['items']> = [...versions].reverse().map((version) => ({
    key: version.id,
    disabled: versionBlocked(version),
    label: `#${Number(version.seq)} · ${formatTime(version.savedAt)}${
      version.id === project.publishedVersionId ? '（当前发布）' : ''
    }`,
  }))

  return (
    <Flex align="center" justify="space-between" gap={12} wrap>
      <Flex align="center" gap={4} wrap>
        <Button
          type="text"
          size="small"
          icon={<ArrowLeft size={16} />}
          onClick={() => void navigate('/galaxy')}
        >
          我的工程
        </Button>
        <ProjectInfoPopover
          project={project}
          canWrite={canWrite}
          onProjectChange={onProjectChange}
        />
      </Flex>

      <Space wrap>
        {assetPanelEnabled && (
          <Button icon={<Images size={16} />} onClick={onOpenAssets}>
            资产
          </Button>
        )}
        {contentEnabled && (
          <Button icon={<History size={16} />} onClick={onOpenVersions}>
            版本
          </Button>
        )}

        {contentEnabled && canWrite && (
          <Button
            type={publishAvailable ? 'default' : 'primary'}
            icon={<Layers size={16} />}
            loading={versionBusy}
            onClick={onSaveVersion}
          >
            存为版本
          </Button>
        )}

        {publishEnabled && canPublish && (
          <Dropdown
            // 默认是 hover 触发，而 hover 在触屏上不存在——手机上就点不开发布。
            trigger={['click']}
            disabled={publishableCount === 0}
            menu={{
              items: publishItems,
              onClick: ({ key }) => {
                const version = versions.find((item) => item.id === key)
                if (version === undefined || versionBlocked(version)) {
                  // 前端拦下、请求根本没发出去——这一点只有客户端事件答得了，
                  // 服务端请求留痕里没有它（见 docs/observability.md）。
                  track({
                    surface: Surface.WEB_EDITOR,
                    action: Action.PUBLISH,
                    result: Result.BLOCKED,
                  })
                  return
                }
                onPublish(key)
              },
            }}
          >
            <Button
              type="primary"
              icon={<Rocket size={16} />}
              loading={publishBusy}
              disabled={publishableCount === 0}
            >
              {project.published ? '更新发布' : '发布'}
              <ChevronDown size={14} />
            </Button>
          </Dropdown>
        )}
      </Space>
    </Flex>
  )
}

/**
 * 清单的可比签名：路径加上字节从哪来。
 *
 * 顺序不影响「是不是同一份清单」。地址不参与——它每次读取都会变，不是内容。
 */
function manifestSignature(entries: readonly FileEntry[]): string {
  return entries
    .map((entry) => {
      const source =
        entry.source.case === 'digest'
          ? `digest:${entry.source.value}`
          : entry.source.case === 'assetId'
            ? `asset:${entry.source.value}`
            : 'missing'
      return `${entry.path}\0${source}`
    })
    .sort()
    .join('\n')
}
