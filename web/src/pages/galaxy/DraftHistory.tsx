import { useState } from 'react'
import { Alert, Button, Empty, Popconfirm, Space, Table, Tag, Typography } from 'antd'
import type { TableProps } from 'antd'
import { RotateCcw } from 'lucide-react'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { ContentSlot, DraftSnapshot } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { track } from '../../telemetry/track'
import { formatTime } from './format-time'

interface DraftHistoryProps {
  projectId: string
  /** 这是**哪个内容槽**的历史。历史按槽隔离。 */
  slot: ContentSlot
  snapshots: readonly DraftSnapshot[]
  /** 是否持有写权限。无权限时不渲染恢复入口。 */
  canWrite: boolean
  /** 恢复成功之后重新拉取草稿、版本与历史。 */
  onRestored: () => Promise<void>
}

interface failure {
  message: string
  traceId: string | null
}

/**
 * 草稿历史：**被替换掉的那些清单**，最近的在前。
 *
 * 它不是版本列表的第二页：这些条目没有序号、**不能发布**，而且会过期（每个槽最近
 * 50 条、且不超过 14 天）。它回答的是"我刚推掉的那一份长什么样"——那正是"只推
 * 不存版本"这条用法唯一的兜底（见 docs/design/galaxy/project-versioning.md）。
 *
 * **恢复是一次整组替换**，走的是与命令行 push 同一条路径，因此当前那份也会被留成
 * 一条新的历史记录：恢复不会让人丢掉恢复前的内容。这也正是网页端可以有这个按钮的
 * 原因——它不是编辑，而是一次显式的、可回退的替换。
 */
export function DraftHistory({
  projectId,
  slot,
  snapshots,
  canWrite,
  onRestored,
}: DraftHistoryProps): React.ReactNode {
  const [failure, setFailure] = useState<failure | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)

  async function handleRestore(snapshotId: string): Promise<void> {
    setBusyId(snapshotId)
    setFailure(null)
    try {
      await galaxyApi.restoreDraftSnapshot(projectId, slot, snapshotId)
      track({ surface: Surface.WEB_EDITOR, action: Action.DRAFT_RESTORE, result: Result.OK })
      await onRestored()
    } catch (err) {
      // 典型的拒绝原因是"这份历史引用了一个已经删掉的素材"，服务端的错误信息
      // 会指出是哪一条路径引用了哪个资产。
      track({ surface: Surface.WEB_EDITOR, action: Action.DRAFT_RESTORE, result: Result.FAIL })
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusyId(null)
    }
  }

  const columns: NonNullable<TableProps<DraftSnapshot>['columns']> = [
    {
      title: '时间',
      dataIndex: 'createdAt',
      key: 'createdAt',
      render: (value: string) => formatTime(value),
    },
    {
      title: '来源',
      dataIndex: 'source',
      key: 'source',
      render: (value: string) => <Tag style={{ marginInlineEnd: 0 }}>{describeSource(value)}</Tag>,
    },
    {
      title: '文件数',
      key: 'files',
      render: (_: unknown, snapshot) => snapshot.entries.length,
    },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, snapshot) =>
        canWrite && (
          <Popconfirm
            title="把草稿换回这一份？"
            description="当前那一份会留成一条新的历史记录，因此现在的内容不会丢。"
            okText="恢复"
            onConfirm={() => void handleRestore(snapshot.id)}
          >
            <Button
              type="link"
              size="small"
              icon={<RotateCcw size={14} />}
              loading={busyId === snapshot.id}
            >
              恢复
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
      <Typography.Text type="secondary">
        每次推送草稿会留下一条（相同清单不重复留），最近 50 条、最多 14 天。它们
        <b>不是版本</b>：不能发布，也会过期——要留下不会过期的一份，还是得存版本。
      </Typography.Text>
      <Table<DraftSnapshot>
        rowKey="id"
        columns={columns}
        dataSource={[...snapshots]}
        pagination={false}
        size="small"
        scroll={{ x: 'max-content' }}
        expandable={{
          // 展开即见清单：路径与条目类别，不取字节（与版本列表同一条理由）。
          expandedRowRender: (snapshot) => (
            <Space direction="vertical" size={2} style={{ width: '100%' }}>
              {snapshot.entries.map((entry) => (
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
          rowExpandable: (snapshot) => snapshot.entries.length > 0,
        }}
        locale={{
          emptyText: (
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description="还没有草稿历史。推送草稿会在这里留下被替换掉的那一份。"
            />
          ),
        }}
      />
    </Space>
  )
}

/**
 * 把快照的来源写成人读的一小段。
 *
 * 空值不是"没有来源"这句话的省略：它表示那一端没有带上报端标识（老版本的命令行、
 * 或第三方客户端），因此要如实说出来，而不是猜一个。
 */
function describeSource(source: string): string {
  switch (source) {
    case 'web':
      return '网页端'
    case 'cli':
      return '命令行'
    default:
      return '未知来源'
  }
}
