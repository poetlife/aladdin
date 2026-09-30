import { Button, Divider, Flex, Popconfirm, Popover, Typography, theme } from 'antd'
import { CircleCheck, TriangleAlert, Undo2 } from 'lucide-react'

import type { ProjectSlot, Version } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { track } from '../../telemetry/track'
import { describeValidation, type ValidationState } from './validation-state'

interface LifecycleStripProps {
  validation: ValidationState
  versions: readonly Version[]
  /**
   * 当前看的内容槽与它的发布状态。
   *
   * **状态条说的就是这个槽**：另一个槽发没发出去、有没有版本，与这一条上的每一句
   * 都无关（见 docs/design/galaxy/authoring.md 的"这一页一次看一个内容槽"）。
   */
  slot: ProjectSlot | undefined
  /** 是否渲染发布那一半。未启用发布时整段不出现（见 spec：不渲染发布入口）。 */
  publishEnabled: boolean
  /** 是否持有发布权限：决定已发布时有没有撤回。 */
  canPublish: boolean
  publishBusy: boolean
  /** 校验没跑成时的重试入口。 */
  onRetryValidate: () => void
  onUnpublish: () => void
  /**
   * 是否窄屏（见 docs/design/web/responsive.md 的「工作台窄屏」）。
   *
   * 窄屏时这一条压成一行，且**不常驻整条发布地址**：地址换成短标签 + 一颗常显的
   * 复制图标（复制的内容仍是那条真实地址）。撤回留在原处、仍是按钮级的次要动作。
   */
  narrow: boolean
}

/**
 * 状态条：**这份草稿能不能发布**，以及**发布出去的是什么**。
 *
 * 只有这两件事值得占一条常驻的行。草稿与版本的保存时间不在这里——它们不驱动任何
 * 决定。命令行改过草稿之后，结论会在页面重新可见时重新问服务端；版本的时间在版本
 * 面板里，点一下就有。因此这里不放标签、不放时间戳，也不要框：一行两段，中间一道竖线。
 *
 * 它只呈现状态：推进流程的动作在顶栏（存版本、选择哪一版发布）。唯一的两个例外
 * 都是有意的——**问题的逐条清单**从结论那里展开（清单属于那份结论本身），
 * **撤回**紧挨着它要作废的那个地址（它是那一处状态的逆操作，不是第二个发布入口）。
 */
export function LifecycleStrip({
  validation,
  versions,
  slot,
  publishEnabled,
  canPublish,
  publishBusy,
  onRetryValidate,
  onUnpublish,
  narrow,
}: LifecycleStripProps): React.ReactNode {
  const { token } = theme.useToken()
  const conclusion = describeValidation(validation)

  const conclusionNode = (
    <Flex align="center" gap={4}>
      {conclusion.tone === 'success' ? (
        <CircleCheck size={16} color={token.colorSuccess} />
      ) : conclusion.tone === 'warning' ? (
        <TriangleAlert size={16} color={token.colorWarning} />
      ) : null}
      {validation.status === 'problems' ? (
        <Popover
          title="这份草稿有以下问题，发布会被拒绝"
          content={
            <ul style={{ margin: 0, paddingInlineStart: 20, maxWidth: 420 }}>
              {validation.problems.map((problem, index) => (
                <li key={index}>
                  {/* 位置（哪一份文件、哪一行）由服务端给出：只说"有引用不合法"
                      会让用户在一组文件里自己找。 */}
                  {problem.path !== '' && (
                    <Typography.Text code style={{ fontSize: 12 }}>
                      {problem.line > 0 ? `${problem.path}:${problem.line}` : problem.path}
                    </Typography.Text>
                  )}{' '}
                  {problem.message}
                </li>
              ))}
            </ul>
          }
          trigger="click"
        >
          <Typography.Text
            type="warning"
            style={{ cursor: 'pointer', textDecoration: 'underline dotted' }}
          >
            {conclusion.text}
          </Typography.Text>
        </Popover>
      ) : (
        <Typography.Text type={conclusion.tone}>{conclusion.text}</Typography.Text>
      )}
      {validation.status === 'failed' && (
        <Button type="link" size="small" onClick={onRetryValidate}>
          重试
        </Button>
      )}
    </Flex>
  )

  const unpublishButton = (
    <Popconfirm
      title="撤回发布？"
      description="这个地址会立刻不可达。发布记录保留，之后可以重新发布同一个版本。"
      okText="撤回"
      okButtonProps={{ danger: true }}
      onConfirm={onUnpublish}
      // 取消：动作被主动放弃，与"失败"分开记——两者的修复方向不同。
      onCancel={() =>
        track({ surface: Surface.WEB_EDITOR, action: Action.UNPUBLISH, result: Result.CANCEL })
      }
    >
      {/* **按钮级，不是文字链接。** 同一排里"发布"是实心按钮、撤回是一
          行小字时，人得先认出那行字能点才会去点——"能力已经有、却像没
          有"正是这么来的。描边的次要按钮在体量上仍然服从那条原则：它是
          那一处状态的逆操作，不是第二个发布入口（见
          docs/design/galaxy/authoring.md）。 */}
      <Button
        size="small"
        danger
        loading={publishBusy}
        // 窄屏只剩图标，名字靠 aria-label 保住（宽屏仍是有字的那颗）。
        aria-label={narrow ? '撤回发布' : undefined}
      >
        {narrow ? <Undo2 size={14} /> : '撤回发布'}
      </Button>
    </Popconfirm>
  )

  // 已发布那一半：宽屏常驻整条地址；窄屏换成短标签 + 一颗常显的复制图标——
  // 复制的内容仍是那条真实地址，显示的不必是它（完整地址在工程信息弹层里也有）。
  const published = narrow ? (
    <Flex align="center" gap={4} wrap={false}>
      <Typography.Text type="success" copyable={{ text: slot?.publishedUrl ?? '' }}>
        已发布
      </Typography.Text>
      {canPublish && unpublishButton}
    </Flex>
  ) : (
    <Flex align="center" gap={4} wrap>
      <Typography.Text type="success">已发布</Typography.Text>
      <Typography.Text
        copyable
        ellipsis={{ tooltip: slot?.publishedUrl }}
        style={{ maxWidth: 260 }}
      >
        {slot?.publishedUrl}
      </Typography.Text>
      {canPublish && unpublishButton}
    </Flex>
  )

  return (
    <Flex align="center" gap={narrow ? 8 : 12} wrap={!narrow}>
      {conclusionNode}

      {publishEnabled && (
        <>
          <Divider type="vertical" style={{ margin: 0 }} />
          {slot?.published === true ? (
            published
          ) : (
            <Typography.Text type="secondary">
              {versions.length === 0 ? '还没有版本，存一版就能发布' : '尚未发布'}
            </Typography.Text>
          )}
        </>
      )}
    </Flex>
  )
}
