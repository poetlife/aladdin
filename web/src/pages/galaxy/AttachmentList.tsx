import { useState } from 'react'
import { Alert, Button, Empty, Form, Input, Popconfirm, Space, Table, Typography, Upload } from 'antd'
import type { TableProps } from 'antd'
import { Copy, Download, FileUp, Pencil, Trash2 } from 'lucide-react'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { Attachment } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { describeBytes } from '../../format/bytes'
import { track } from '../../telemetry/track'
import { AppModal } from '../../ui/AppModal'
import { sha256Hex } from '../../upload/content-digest'
import { directUpload } from '../../upload/direct-upload'
import { formatTime } from './format-time'

interface AttachmentListProps {
  projectId: string
  /**
   * 单份附件的上限。**只用来省一次往返**：超限与否的权威判定在服务端。
   *
   * 它取自能力下发的 `max_attachment_bytes`（proto 的 uint64 → bigint），而
   * `describeBytes` 两个都吃（见 format/bytes.ts）。
   */
  maxBytes: number | bigint
  attachments: readonly Attachment[]
  /** 是否持有上传/删除/改说明的权限。无权限时不渲染这几个入口。 */
  canWrite: boolean
  /** 上传、删除或改说明成功之后重新拉取附件清单（含新的短时地址）。 */
  onChanged: () => Promise<void>
}

interface failure {
  message: string
  traceId: string | null
}

/**
 * 工程附件面板（弹层内容）：上传、下载、复制摘要、改说明、删除。
 *
 * **它与资产面板并列而不合并**：附件是给成员下载的发布物，类型不限、永不进公开区
 * （见 docs/design/galaxy/attachments.md）。因此这里没有媒体预览、没有标签与筛选，
 * 列出来的是"这一份是什么、多大、对应哪一版"。
 *
 * 这里的每一次判断都只是**省一次往返**或**决定要不要渲染**，不是安全边界：上限由
 * 服务端与存储侧判定，能不能删也由服务端说了算。
 *
 * 上传是三步：算摘要 → 取直传凭证 → 直传到对象存储 → 提交，服务端不接触字节。
 * `beforeUpload` 返回 false 阻止 antd 自己发起上传——它会把文件 PUT 到一个我们
 * 没配的地址（与资产面板同一取向）。
 */
export function AttachmentList({
  projectId,
  maxBytes,
  attachments,
  canWrite,
  onChanged,
}: AttachmentListProps): React.ReactNode {
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<failure | null>(null)
  // 最近一次失败的文件：留着它，用户点「重试」时能原样再来一次。
  const [retryFile, setRetryFile] = useState<File | null>(null)
  const [copiedId, setCopiedId] = useState<string | null>(null)

  // 正在改说明的那一份。null 表示弹层关着。
  const [editing, setEditing] = useState<Attachment | null>(null)
  const [savingDescription, setSavingDescription] = useState(false)
  const [descriptionFailure, setDescriptionFailure] = useState<failure | null>(null)
  const [descriptionForm] = Form.useForm<{ description: string }>()

  /**
   * 打开说明编辑弹层。
   *
   * 每次都用**当前值**重置表单：上一次编到一半又关掉的内容不该留到下一次。
   */
  function openDescriptionEditor(attachment: Attachment): void {
    setDescriptionFailure(null)
    setEditing(attachment)
    descriptionForm.setFieldsValue({ description: attachment.description })
  }

  /** 保存说明。请求表达的是**期望的完整状态**，因此把表单里的那一个字段整组发出去。 */
  async function handleSaveDescription(): Promise<void> {
    if (editing === null) {
      return
    }
    let values
    try {
      values = await descriptionForm.validateFields()
    } catch {
      track({
        surface: Surface.WEB_EDITOR,
        action: Action.ATTACHMENT_META_SAVE,
        result: Result.BLOCKED,
      })
      return
    }
    setSavingDescription(true)
    setDescriptionFailure(null)
    try {
      await galaxyApi.updateAttachment(projectId, editing.id, values.description ?? '')
      setEditing(null)
      await onChanged()
    } catch (err) {
      setDescriptionFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setSavingDescription(false)
    }
  }

  async function handleFile(file: File): Promise<void> {
    if (file.size > maxBytes) {
      // 按声明的上限在本地早退：请求没发出去，服务端留痕里没有这次尝试。
      track({
        surface: Surface.WEB_EDITOR,
        action: Action.ATTACHMENT_UPLOAD,
        result: Result.BLOCKED,
      })
      setFailure({ message: `单份附件不能超过 ${describeBytes(maxBytes)}`, traceId: null })
      // 超限重试同一个文件没有意义，不给重试入口。
      setRetryFile(null)
      return
    }

    setBusy(true)
    setFailure(null)
    setRetryFile(null)
    try {
      // 摘要必须在提交前算好：它是那份字节的标识，服务端没有字节可以算它，而它会
      // 在提交时被核对（见 docs/design/galaxy/attachments.md）。
      const digest = await sha256Hex(await file.arrayBuffer())
      // 标注的版本留空：网页端不改内容，因此"这一份是哪一版的产物"由命令行
      // （upload --save-version）回答，比在这里让人手挑一个版本准确。
      const begin = await galaxyApi.beginAttachmentUpload(projectId, '', file.size)
      if (begin.upload === undefined) {
        throw new Error('服务端没有返回直传凭证')
      }
      // 上传声明中性类型：附件的下发类型与它无关，服务端签发策略时用的就是它。
      await directUpload(begin.upload, file, 'application/octet-stream')
      await galaxyApi.commitAttachmentUpload(
        projectId,
        begin.attachmentId,
        '',
        digest,
        file.name,
      )
      await onChanged()
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      setRetryFile(file)
    } finally {
      setBusy(false)
    }
  }

  /**
   * 下载一份附件。
   *
   * **先重新签一次地址再打开**：列表里那一条是打开面板时签的，只有几分钟；用户
   * 隔一会儿再点就该拿一条新的。签名失败时退回列表里那一条，而不是拒绝这次点击。
   */
  async function handleDownload(attachment: Attachment): Promise<void> {
    let url = attachment.downloadUrl
    try {
      const signed = await galaxyApi.getAttachmentDownloadURL(projectId, attachment.id)
      if (signed.url !== '') {
        url = signed.url
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      return
    }
    if (url === '') {
      setFailure({ message: '取不到下载地址，可能是这个部署没有配置对象存储', traceId: null })
      return
    }
    // 响应头被服务端固定成强制下载，所以这里不需要 download 属性去补那件事。
    window.open(url, '_blank', 'noopener')
  }

  async function handleDelete(attachmentId: string): Promise<void> {
    setBusy(true)
    setFailure(null)
    try {
      await galaxyApi.deleteAttachment(projectId, attachmentId)
      await onChanged()
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusy(false)
    }
  }

  async function handleCopyDigest(attachment: Attachment): Promise<void> {
    try {
      await navigator.clipboard?.writeText(attachment.digest)
      setCopiedId(attachment.id)
    } catch {
      // 剪贴板不可用（无权限、非安全上下文）时不做特殊处理：用户仍可手动选中。
      setCopiedId(null)
    }
  }

  const columns: NonNullable<TableProps<Attachment>['columns']> = [
    {
      title: '文件',
      key: 'filename',
      render: (_: unknown, attachment) => (
        <Space orientation="vertical" size={0}>
          <Typography.Text ellipsis={{ tooltip: attachment.filename }}>
            {attachment.filename === '' ? '(未命名)' : attachment.filename}
          </Typography.Text>
          {attachment.description !== '' && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {attachment.description}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: '大小',
      key: 'size',
      render: (_: unknown, attachment) => describeBytes(attachment.sizeBytes),
    },
    {
      title: '标注版本',
      key: 'version',
      render: (_: unknown, attachment) =>
        attachment.versionId === '' ? (
          <Typography.Text type="secondary">—</Typography.Text>
        ) : (
          // 版本标识由命令行给出，网页端不把序号查出来：标注说的是"哪一份构建产物"，
          // 而标识本身就是那个答案。
          <Typography.Text code style={{ fontSize: 12 }}>
            {attachment.versionId}
          </Typography.Text>
        ),
    },
    {
      title: '上传时间',
      dataIndex: 'uploadedAt',
      key: 'uploadedAt',
      render: (value: string) => formatTime(value),
    },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, attachment) => (
        <Space size={4} wrap>
          <Button
            type="link"
            size="small"
            icon={<Download size={14} />}
            onClick={() => void handleDownload(attachment)}
          >
            下载
          </Button>
          <Button
            type="link"
            size="small"
            icon={<Copy size={14} />}
            onClick={() => void handleCopyDigest(attachment)}
          >
            {copiedId === attachment.id ? '已复制' : '复制 sha256'}
          </Button>
          {canWrite && (
            <Button
              type="link"
              size="small"
              icon={<Pencil size={14} />}
              onClick={() => openDescriptionEditor(attachment)}
            >
              改说明
            </Button>
          )}
          {canWrite && (
            <Popconfirm
              title="删除这份附件？"
              description="字节删掉就没有了。附件不被任何引用拦阻，因此不会被拒。"
              okText="删除"
              okButtonProps={{ danger: true }}
              onConfirm={() => void handleDelete(attachment.id)}
            >
              <Button type="link" size="small" danger icon={<Trash2 size={14} />}>
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
          action={
            retryFile === null ? null : (
              <Button size="small" onClick={() => void handleFile(retryFile)}>
                重试
              </Button>
            )
          }
        />
      )}

      {canWrite && (
        <Space orientation="vertical" size={4} style={{ width: '100%' }}>
          <Upload
            showUploadList={false}
            beforeUpload={(file) => {
              void handleFile(file)
              return false
            }}
          >
            <Button icon={<FileUp size={16} />} loading={busy}>
              上传附件
            </Button>
          </Upload>
          <Typography.Text type="secondary">
            二进制、zip、导出文件都可以，单份不超过 {describeBytes(maxBytes)}。类型不限——下载时
            响应头由服务端固定成<b>强制下载</b>，因此不管文件是什么都只会被存下来。
          </Typography.Text>
          <Typography.Text type="secondary">
            附件<b>不进公开区</b>：发布出去的是页面，它只给这个工程的成员下载。下载地址是短时凭证，
            别转发给不该拿到它的人。
          </Typography.Text>
        </Space>
      )}

      <Table<Attachment>
        rowKey="id"
        columns={columns}
        dataSource={[...attachments]}
        pagination={false}
        size="small"
        scroll={{ x: 'max-content' }}
        locale={{
          emptyText: (
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description="还没有附件。构建产物、zip 包可以放在这里。"
            />
          ),
        }}
      />

      <AppModal
        title="改附件说明"
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
            extra="留空表示没有说明。改了它不影响字节，已经发出去的下载地址指向的还是同一份文件"
          >
            <Input.TextArea rows={3} maxLength={512} showCount />
          </Form.Item>
        </Form>
      </AppModal>
    </Space>
  )
}
