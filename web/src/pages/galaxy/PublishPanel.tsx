import { useState } from 'react'
import { Alert, Button, Empty, Popconfirm, Select, Space, Typography } from 'antd'
import { Rocket, Undo2 } from 'lucide-react'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { Project, Version } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { formatTime } from './format-time'

interface PublishPanelProps {
  projectId: string
  project: Project
  versions: readonly Version[]
  /** 是否持有发布权限（galaxy.project.publish）。 */
  canPublish: boolean
  /** 发布/撤回生效之后的新工程状态。 */
  onProjectChange: (project: Project) => void
}

interface failure {
  message: string
  traceId: string | null
}

/**
 * 发布入口。
 *
 * **只在 capabilities.publishEnabled 为真时才被渲染**（由调用方决定）：
 * 未配置公开桶或发布域时，前端不渲染入口，而不是渲染一个点了报错的控件。
 *
 * 发布**只能发布版本，不能发布草稿**（见 docs/design/galaxy/publication.md），
 * 因此这里的选择项只有版本。撤回是发布的反向操作，用同一个权限码，同样要
 * 二次确认——撤回之后地址立刻不可达。
 */
export function PublishPanel({
  projectId,
  project,
  versions,
  canPublish,
  onProjectChange,
}: PublishPanelProps): React.ReactNode {
  const latest = versions.length === 0 ? undefined : versions[versions.length - 1]
  const [selectedVersionId, setSelectedVersionId] = useState<string>(
    project.publishedVersionId !== '' ? project.publishedVersionId : (latest?.id ?? ''),
  )
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<failure | null>(null)

  async function handlePublish(): Promise<void> {
    if (selectedVersionId === '') {
      return
    }
    setBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.publish(projectId, selectedVersionId)
      if (response.project !== undefined) {
        onProjectChange(response.project)
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusy(false)
    }
  }

  async function handleUnpublish(): Promise<void> {
    setBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.unpublish(projectId)
      if (response.project !== undefined) {
        onProjectChange(response.project)
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusy(false)
    }
  }

  if (!canPublish) {
    return null
  }

  return (
    <Space orientation="vertical" size={12} style={{ width: '100%' }}>
      {failure !== null && (
        <Alert
          type="error"
          message={failure.message}
          description={
            failure.traceId !== null && (
              <Typography.Text type="secondary" copyable>
                追踪 ID：{failure.traceId}
              </Typography.Text>
            )
          }
        />
      )}

      {project.published ? (
        <Alert
          type="success"
          showIcon
          message="已发布"
          description={
            <Space orientation="vertical" size={4}>
              <Typography.Text copyable style={{ wordBreak: 'break-all' }}>
                {project.publishedUrl}
              </Typography.Text>
              <Typography.Text type="secondary">
                生效时间：{formatTime(project.publishedAt)}
              </Typography.Text>
              <Typography.Text type="secondary">
                地址即凭据：拿到地址的人都能看，未登录也可访问。
              </Typography.Text>
            </Space>
          }
        />
      ) : (
        <Typography.Text type="secondary">
          尚未发布。发布后未登录的人也能用发布地址打开这个页面。
        </Typography.Text>
      )}

      {versions.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有版本，先保存一个版本再发布" />
      ) : (
        <Space wrap>
          <Select
            style={{ minWidth: 220 }}
            value={selectedVersionId}
            onChange={setSelectedVersionId}
            options={versions.map((version) => ({
              value: version.id,
              label: `#${Number(version.seq)} · ${formatTime(version.savedAt)}`,
            }))}
          />
          <Button
            type="primary"
            icon={<Rocket size={16} />}
            loading={busy}
            onClick={() => void handlePublish()}
          >
            {project.published ? '更新发布' : '发布这个版本'}
          </Button>
          {project.published && (
            <Popconfirm
              title="撤回发布？"
              description="发布地址会立刻不可达。发布记录保留，之后可以重新发布同一个版本。"
              okText="撤回"
              okButtonProps={{ danger: true }}
              onConfirm={() => void handleUnpublish()}
            >
              <Button danger icon={<Undo2 size={16} />} loading={busy}>
                撤回发布
              </Button>
            </Popconfirm>
          )}
        </Space>
      )}
    </Space>
  )
}
