import { useState } from 'react'
import { Alert, Button, Empty, Form, Input, Popconfirm, Space, Table, Tag, Typography } from 'antd'
import type { TableProps } from 'antd'
import { Pencil, Trash2 } from 'lucide-react'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { ContentSlot, Version } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { AppModal } from '../../ui/AppModal'
import { formatTime } from './format-time'

interface VersionListProps {
  projectId: string
  /** 这个列表是**哪个内容槽**的。版本按槽隔离，删除也要指名道姓。 */
  slot: ContentSlot
  versions: readonly Version[]
  /** 是否持有写权限。无权限时不渲染删除与改说明的入口。 */
  canWrite: boolean
  /** 删除或改说明成功之后重新拉取版本列表。 */
  onChanged: () => Promise<void>
}

interface failure {
  message: string
  traceId: string | null
}

/** 与服务端的上限同值（见 docs/design/galaxy/project-versioning.md）。 */
const DESCRIPTION_MAX_RUNES = 280

/**
 * 版本列表：序号 + 说明 + 文件数 + 时间，可展开看清单、可删除。
 *
 * **清单随列表一起来**（它只有路径与摘要，几 KB 量级），因此"这一版有哪些文件"
 * 不需要额外的一次调用——展开即见。网页端不改内容，所以这里没有"读回成草稿"这类
 * 动作：把一版的内容重新变成可编辑的草稿属于内容的写入，那条路只有命令行。
 *
 * **说明可以在这里补写**：它是元数据层，改它不动清单，也不影响已发布的页面
 * （见 docs/design/galaxy/project-versioning.md）。写错一句话不必再存一版。
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

  // 正在补写说明的那一版。null 表示弹层关着。
  const [editing, setEditing] = useState<Version | null>(null)
  const [savingDescription, setSavingDescription] = useState(false)
  const [descriptionFailure, setDescriptionFailure] = useState<failure | null>(null)
  const [descriptionForm] = Form.useForm<{ description: string }>()

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

  /** 打开说明编辑弹层，每次都用**当前值**重置表单。 */
  function openDescriptionEditor(version: Version): void {
    setDescriptionFailure(null)
    setEditing(version)
    descriptionForm.setFieldsValue({ description: version.description })
  }

  /** 保存说明。**只发说明这一个字段**：这一版的内容不在这条路径上。 */
  async function handleSaveDescription(): Promise<void> {
    if (editing === null) {
      return
    }
    let values
    try {
      values = await descriptionForm.validateFields()
    } catch {
      // 表单校验没过时请求根本没发出去——不当作失败处理。
      return
    }
    setSavingDescription(true)
    setDescriptionFailure(null)
    try {
      await galaxyApi.updateVersion(projectId, slot, editing.id, values.description ?? '')
      setEditing(null)
      await onChanged()
    } catch (err) {
      setDescriptionFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setSavingDescription(false)
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
      title: '说明',
      key: 'description',
      render: (_: unknown, version) =>
        version.description === '' ? (
          canWrite ? (
            <Button type="link" size="small" onClick={() => openDescriptionEditor(version)}>
              补写说明
            </Button>
          ) : (
            <Typography.Text type="secondary">—</Typography.Text>
          )
        ) : (
          <Space size={4}>
            <Typography.Text ellipsis={{ tooltip: version.description }}>
              {version.description}
            </Typography.Text>
            {canWrite && (
              <Button
                type="text"
                size="small"
                icon={<Pencil size={12} />}
                aria-label="改说明"
                onClick={() => openDescriptionEditor(version)}
              />
            )}
          </Space>
        ),
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

      <AppModal
        title="版本说明"
        open={editing !== null}
        onCancel={() => setEditing(null)}
        onOk={() => void handleSaveDescription()}
        okText="保存"
        confirmLoading={savingDescription}
        destroyOnHidden
      >
        {descriptionFailure !== null && (
          <Alert
            type="error"
            title={descriptionFailure.message}
            description={
              descriptionFailure.traceId !== null && (
                <Typography.Text type="secondary" copyable>
                  追踪 ID：{descriptionFailure.traceId}
                </Typography.Text>
              )
            }
            style={{ marginBottom: 12 }}
          />
        )}
        <Form form={descriptionForm} layout="vertical">
          <Form.Item
            name="description"
            label="说明"
            extra="留空表示没有说明。改了它不影响这一版的内容，已发布的页面也不会变"
          >
            <Input.TextArea rows={3} maxLength={DESCRIPTION_MAX_RUNES} showCount />
          </Form.Item>
        </Form>
      </AppModal>
    </Space>
  )
}
