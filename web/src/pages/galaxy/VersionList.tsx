import { useState } from 'react'
import { Alert, Button, Empty, Popconfirm, Space, Table, Tag, Typography } from 'antd'
import type { TableProps } from 'antd'
import { Trash2 } from 'lucide-react'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { ContentSlot, Version } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { formatTime } from './format-time'

interface VersionListProps {
  projectId: string
  /** 这个列表是**哪个内容槽**的。版本按槽隔离，删除也要指名道姓。 */
  slot: ContentSlot
  versions: readonly Version[]
  /** 是否持有写权限。无权限时不渲染删除入口。 */
  canWrite: boolean
  /** 删除成功之后重新拉取版本列表。 */
  onChanged: () => Promise<void>
}

interface failure {
  message: string
  traceId: string | null
}

/**
 * 版本列表：序号 + 文件数 + 时间，可展开看清单、可删除。
 *
 * **清单随列表一起来**（它只有路径与摘要，几 KB 量级），因此"这一版有哪些文件"
 * 不需要额外的一次调用——展开即见。网页端只读，所以这里没有"读回成草稿"这类
 * 动作：把一版的内容重新变成可编辑的草稿属于内容的写入，那条路只有命令行。
 *
 * **被当前发布指向的版本不可删**——这条判断只有服务端有，前端不预判；点了
 * 删除之后把服务端的拒绝原因原样呈现出来（见 docs/design/galaxy/project-versioning.md）。
 */
export function VersionList({
  projectId,
  slot,
  versions,
  canWrite,
  onChanged,
}: VersionListProps): React.ReactNode {
  const [failure, setFailure] = useState<failure | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)

  async function handleDelete(versionId: string): Promise<void> {
    setBusyId(versionId)
    setFailure(null)
    try {
      await galaxyApi.deleteVersion(projectId, slot, versionId)
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
      title: '文件数',
      key: 'files',
      render: (_: unknown, version) => version.entries.length,
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
      render: (_: unknown, version) =>
        canWrite && (
          <Popconfirm
            title="删除这个版本？"
            description="被当前发布指向的版本不能删除，否则发布地址会指向一个不存在的版本。"
            okText="删除"
            okButtonProps={{ danger: true }}
            onConfirm={() => void handleDelete(version.id)}
          >
            <Button type="link" danger icon={<Trash2 size={14} />} loading={busyId === version.id}>
              删除
            </Button>
          </Popconfirm>
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
        expandable={{
          // 展开即见清单：路径与条目类别，不取字节（读字节的地址是短时的，
          // 列表不该下发一批会过期的字符串）。
          expandedRowRender: (version) => (
            <Space direction="vertical" size={2} style={{ width: '100%' }}>
              {version.entries.map((entry) => (
                <Space key={entry.path} size={8}>
                  <Typography.Text style={{ fontSize: 12 }}>{entry.path}</Typography.Text>
                  {entry.source.case === 'assetId' && (
                    <Tag style={{ marginInlineEnd: 0 }} color="blue">
                      资产
                    </Tag>
                  )}
                </Space>
              ))}
            </Space>
          ),
          rowExpandable: (version) => version.entries.length > 0,
        }}
        locale={{
          emptyText: (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有保存过版本" />
          ),
        }}
      />
    </Space>
  )
}
