import { useState } from 'react'
import { Alert, Button, Empty, Popconfirm, Space, Table, Typography } from 'antd'
import type { TableProps } from 'antd'
import { Eye, Trash2 } from 'lucide-react'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { Version } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { formatTime } from './format-time'

interface VersionListProps {
  projectId: string
  versions: readonly Version[]
  /** 是否持有写权限。无权限时不渲染删除入口。 */
  canWrite: boolean
  /** 读回一个版本的正文，交由编辑器载入。 */
  onReadBack: (content: string, seq: number) => void
  /** 删除成功之后重新拉取版本列表。 */
  onChanged: () => Promise<void>
}

interface failure {
  message: string
  traceId: string | null
}

/**
 * 版本列表：序号 + 时间，可读回、可删除。
 *
 * **被当前发布指向的版本不可删**——这条判断只有服务端有，前端不预判；点了
 * 删除之后把服务端的拒绝原因原样呈现出来（见 docs/design/galaxy/project-versioning.md）。
 * 列表接口不带正文，读回时才用 GetVersion 单独取。
 */
export function VersionList({
  projectId,
  versions,
  canWrite,
  onReadBack,
  onChanged,
}: VersionListProps): React.ReactNode {
  const [failure, setFailure] = useState<failure | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)

  async function handleReadBack(version: Version): Promise<void> {
    setBusyId(version.id)
    setFailure(null)
    try {
      const response = await galaxyApi.getVersion(projectId, version.id)
      if (response.version !== undefined) {
        onReadBack(response.version.content, Number(response.version.seq))
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusyId(null)
    }
  }

  async function handleDelete(versionId: string): Promise<void> {
    setBusyId(versionId)
    setFailure(null)
    try {
      await galaxyApi.deleteVersion(projectId, versionId)
      await onChanged()
    } catch (err) {
      // 删除被拒的典型原因是"这个版本正在发布中"，服务端的错误信息会说明。
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusyId(null)
    }
  }

  const columns: NonNullable<TableProps<Version>['columns']> = [
    {
      title: '序号',
      dataIndex: 'seq',
      key: 'seq',
      render: (seq: bigint) => <Typography.Text code>#{Number(seq)}</Typography.Text>,
    },
    {
      title: '保存时间',
      dataIndex: 'savedAt',
      key: 'savedAt',
      render: (value: string) => formatTime(value),
    },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, version) => (
        <Space>
          <Button
            type="link"
            icon={<Eye size={14} />}
            loading={busyId === version.id}
            onClick={() => void handleReadBack(version)}
          >
            读回
          </Button>
          {canWrite && (
            <Popconfirm
              title="删除这个版本？"
              description="被当前发布指向的版本不能删除，否则发布地址会指向一个不存在的版本。"
              okText="删除"
              okButtonProps={{ danger: true }}
              onConfirm={() => void handleDelete(version.id)}
            >
              <Button type="link" danger icon={<Trash2 size={14} />}>
                删除
              </Button>
            </Popconfirm>
          )}
        </Space>
      ),
    },
  ]

  return (
    <Space orientation="vertical" size={12} style={{ width: '100%' }}>
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
        />
      )}
      <Table<Version>
        rowKey="id"
        columns={columns}
        dataSource={[...versions]}
        pagination={false}
        size="small"
        scroll={{ x: 'max-content' }}
        locale={{
          emptyText: (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有保存过版本" />
          ),
        }}
      />
    </Space>
  )
}
