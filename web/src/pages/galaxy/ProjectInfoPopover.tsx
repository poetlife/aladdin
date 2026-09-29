import { useState } from 'react'
import { Alert, Button, Divider, Flex, Form, Input, Popover, Typography } from 'antd'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { Project } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { slotDescription, slotLabel } from './content-slot'

interface ProjectFormValues {
  name?: string
  description?: string
}

interface ProjectInfoPopoverProps {
  project: Project
  /** 是否持有写权限。无权限时只展示，不给保存入口。 */
  canWrite: boolean
  /** 保存成功之后的新工程状态。 */
  onProjectChange: (project: Project) => void
}

interface failure {
  message: string
  traceId: string | null
}

/**
 * 工程元数据的编辑入口（名称、简介）。
 *
 * 它原来占着页面顶部一整张卡片，而名称与简介**不参与创作循环**——改一次就用很久。
 * 因此收进一个需要时才打开的弹层，把常驻版面让给预览与状态
 * （见 docs/design/galaxy/authoring.md 的"编辑页的形态"）。
 */
export function ProjectInfoPopover({
  project,
  canWrite,
  onProjectChange,
}: ProjectInfoPopoverProps): React.ReactNode {
  const [form] = Form.useForm<ProjectFormValues>()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<failure | null>(null)

  async function handleSave(values: ProjectFormValues): Promise<void> {
    setBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.updateProject(
        project.id,
        values.name ?? '',
        values.description ?? '',
      )
      if (response.project !== undefined) {
        onProjectChange(response.project)
      }
      setOpen(false)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusy(false)
    }
  }

  const title = project.name === '' ? '(未命名)' : project.name

  const content = (
    <div style={{ width: 280 }}>
      {failure !== null && (
        <Alert
          type="error"
          title={failure.message}
          description={
            failure.traceId !== null && (
              <Typography.Text type="secondary" copyable>
                追踪 ID：{failure.traceId}
              </Typography.Text>
            )
          }
          style={{ marginBottom: 12 }}
        />
      )}
      <Form<ProjectFormValues>
        form={form}
        layout="vertical"
        initialValues={{ name: project.name, description: project.description }}
        onFinish={(values) => void handleSave(values)}
      >
        <Form.Item name="name" label="名称" extra="仅用于你自己识别，不是地址、不需要唯一">
          <Input maxLength={64} disabled={!canWrite} autoComplete="off" />
        </Form.Item>
        <Form.Item name="description" label="简介" extra="可留空">
          <Input.TextArea maxLength={280} rows={3} disabled={!canWrite} />
        </Form.Item>
        {canWrite && (
          <Button type="primary" htmlType="submit" loading={busy} size="small">
            保存
          </Button>
        )}
      </Form>
      {/* 工程标识是不可改的只读信息，跟名称与内容槽同属"这个工程是谁"——因此放这里，
          不占页面上的常驻版面（见 docs/design/galaxy/authoring.md）。 */}
      <Divider style={{ margin: '12px 0 0' }} />
      <Flex vertical gap={2} style={{ marginTop: 12 }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          工程标识
        </Typography.Text>
        <Typography.Text code copyable style={{ wordBreak: 'break-all' }}>
          {project.id}
        </Typography.Text>
      </Flex>

      {/* 启用了哪些槽、每个槽是什么、已发布的地址——与工程标识同属只读的"这是谁"。
          切换与"加一个槽"在顶栏（那是"我在哪里"），这里只陈述现状。 */}
      <Divider style={{ margin: '12px 0 0' }} />
      <Flex vertical gap={6} style={{ marginTop: 12 }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          内容槽
        </Typography.Text>
        {project.slots.length === 0 ? (
          <Typography.Text style={{ fontSize: 12 }}>（一个都没有）</Typography.Text>
        ) : (
          project.slots.map((slot) => (
            <Flex key={slot.slot} vertical gap={0}>
              <Typography.Text strong style={{ fontSize: 12 }}>
                {slotLabel(slot.slot)}
              </Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {slotDescription(slot.slot)}
              </Typography.Text>
              {slot.publishedUrl !== '' && (
                <Typography.Text code copyable style={{ fontSize: 12, wordBreak: 'break-all' }}>
                  {slot.publishedUrl}
                </Typography.Text>
              )}
            </Flex>
          ))
        )}
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          槽只增不删，加一个在顶栏。
        </Typography.Text>
      </Flex>
    </div>
  )

  return (
    <Popover
      content={content}
      trigger="click"
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        setFailure(null)
        // 每次打开都用服务端当前的工程信息重填，不保留上一次编辑到一半的值。
        form.setFieldsValue({ name: project.name, description: project.description })
      }}
    >
      <Button type="text" size="small" aria-label="工程信息">
        <Typography.Text strong style={{ fontSize: 16 }}>
          {title}
        </Typography.Text>
      </Button>
    </Popover>
  )
}
