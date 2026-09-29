import { Button, Divider, Flex, Popconfirm, Popover, Typography, theme } from 'antd'
import { CircleCheck, TriangleAlert } from 'lucide-react'

import type { Project, Version } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { describeValidation, type ValidationState } from './validation-state'

interface LifecycleStripProps {
  validation: ValidationState
  versions: readonly Version[]
  project: Project
  /** 是否渲染发布那一半。未启用发布时整段不出现（见 spec：不渲染发布入口）。 */
  publishEnabled: boolean
  /** 是否持有发布权限：决定已发布时有没有撤回。 */
  canPublish: boolean
  publishBusy: boolean
  /** 校验没跑成时的重试入口。 */
  onRetryValidate: () => void
  onUnpublish: () => void
}

/**
 * 状态条：**这份草稿能不能发布**，以及**发布出去的是什么**。
 *
 * 只有这两件事值得占一条常驻的行。草稿与版本的保存时间不在这里——它们不驱动任何
 * 决定，改没改过由结论本身说（"有改动，保存后重新校验"）；版本的时间在版本面板里，
 * 点一下就有。因此这里不放标签、不放时间戳，也不要框：一行两段，中间一道竖线。
 *
 * 它只呈现状态：推进流程的动作在顶栏（存版本、选择哪一版发布）。唯一的两个例外
 * 都是有意的——**问题的逐条清单**从结论那里展开（清单属于那份结论本身），
 * **撤回**紧挨着它要作废的那个地址（它是那一处状态的逆操作，不是第二个发布入口）。
 */
export function LifecycleStrip({
  validation,
  versions,
  project,
  publishEnabled,
  canPublish,
  publishBusy,
  onRetryValidate,
  onUnpublish,
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
          title="正文有以下问题，发布会被拒绝"
          content={
            <ul style={{ margin: 0, paddingInlineStart: 20, maxWidth: 420 }}>
              {validation.problems.map((problem, index) => (
                <li key={index}>{problem.message}</li>
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

  return (
    <Flex align="center" gap={12} wrap>
      {conclusionNode}

      {publishEnabled && (
        <>
          <Divider type="vertical" style={{ margin: 0 }} />
          {project.published ? (
            <Flex align="center" gap={4} wrap>
              <Typography.Text type="success">已发布</Typography.Text>
              <Typography.Text
                copyable
                ellipsis={{ tooltip: project.publishedUrl }}
                style={{ maxWidth: 260 }}
              >
                {project.publishedUrl}
              </Typography.Text>
              {canPublish && (
                <Popconfirm
                  title="撤回发布？"
                  description="这个地址会立刻不可达。发布记录保留，之后可以重新发布同一个版本。"
                  okText="撤回"
                  okButtonProps={{ danger: true }}
                  onConfirm={onUnpublish}
                >
                  <Button type="link" size="small" danger loading={publishBusy}>
                    撤回
                  </Button>
                </Popconfirm>
              )}
            </Flex>
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
