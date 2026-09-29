import { useState } from 'react'
import {
  Alert,
  Button,
  Empty,
  Form,
  Image,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Tag,
  Typography,
  Upload,
  theme,
} from 'antd'
import { Copy, ImageUp, Pencil, Trash2 } from 'lucide-react'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { Asset, AssetKindLimit } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { track } from '../../telemetry/track'
import { sha256Hex } from '../../upload/content-digest'
import { directUpload } from '../../upload/direct-upload'
import { describeBytes } from '../../format/bytes'
import { formatTime } from './format-time'
import { mediaKindOfDeclaredType } from './media-kind'

// 文件选择框的过滤（见 docs/design/galaxy/asset-library.md 的白名单）；它只影响
// 选择框里能看到什么，**不是校验**：真正的类型由上传方声明、由服务端与存储侧判定。
const ACCEPTED_TYPES =
  'image/png,image/jpeg,image/gif,image/webp,video/mp4,video/webm,audio/mpeg,audio/ogg,audio/wav'

/** 预览区（也是"查看"的落点）的高度。 */
const MEDIA_HEIGHT = 132

interface AssetLibraryProps {
  projectId: string
  capabilities: {
    assetLimits: readonly AssetKindLimit[]
  }
  /** 当前筛选下要展示的资产。 */
  assets: readonly Asset[]
  /** **整个工程**已有的标签，供筛选候选用；不随当前筛选收窄。 */
  projectTags: readonly string[]
  /** 当前选中的标签筛选（在服务端生效）。 */
  filterTags: readonly string[]
  onFilterChange: (tags: string[]) => void
  /** 是否持有上传/删除/编辑权限。无权限时不渲染这几个入口。 */
  canWrite: boolean
  /** 上传、删除或改元数据成功之后重新拉取资产清单（含新的短时地址）。 */
  onChanged: () => Promise<void>
}

interface failure {
  message: string
  traceId: string | null
}

/** 元数据编辑弹层的表单字段。 */
interface MetaFormValues {
  title: string
  tags: string[]
  notes: string
}

// 与服务端的上限同值（见 docs/design/galaxy/asset-library.md）。它们只用来**省
// 一次往返**：超限与否的权威判定在服务端，这里不复写那条判断。
const TITLE_MAX_RUNES = 128
const NOTES_MAX_RUNES = 2048
const MAX_TAGS = 16

/**
 * 工程资产面板（弹层内容）：上传、查看、复制引用、删除。
 *
 * **查看**由 antd 的 `Image` 自带：点一下放大到原图。不做转码、不做缩略图
 * （见 docs/design/galaxy/asset-library.md），因此放大的就是原样的字节。
 *
 * 这里的每一次判断都只是**省一次往返**或**决定要不要渲染**，不是安全边界：
 * 类型与大小由上传方声明、由服务端与存储侧判定，删除是否被拒也由服务端说了算
 * （见 docs/design/objectstore/README.md 与 docs/design/galaxy/asset-library.md）。
 *
 * 上传是三步：算摘要 → 取直传凭证 → 直传到对象存储 → 提交，服务端不接触字节。
 * `beforeUpload` 返回 false 阻止 antd 自己发起上传——它会把文件 PUT 到一个我们
 * 没配的地址（与资料页头像同一取向）。
 */
export function AssetLibrary({
  projectId,
  capabilities,
  assets,
  projectTags,
  filterTags,
  onFilterChange,
  canWrite,
  onChanged,
}: AssetLibraryProps): React.ReactNode {
  const { token } = theme.useToken()
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<failure | null>(null)
  // 最近一次失败的文件：留着它，用户点「重试」时能原样再来一次。上传失败
  // （网络中断、提交报"上传没有完成"）必须有一条可重试的提示，不许静默。
  const [retryFile, setRetryFile] = useState<File | null>(null)
  const [copiedId, setCopiedId] = useState<string | null>(null)

  // 正在编辑元数据的资产。null 表示弹层关着。
  const [editing, setEditing] = useState<Asset | null>(null)
  const [savingMeta, setSavingMeta] = useState(false)
  const [metaFailure, setMetaFailure] = useState<failure | null>(null)
  const [metaForm] = Form.useForm<MetaFormValues>()

  /**
   * 打开元数据编辑弹层。
   *
   * 每次都用**当前值**重置表单：上一次编到一半又关掉的内容不该留到下一次。
   */
  function openMetaEditor(asset: Asset): void {
    setMetaFailure(null)
    setEditing(asset)
    metaForm.setFieldsValue({ title: asset.title, tags: [...asset.tags], notes: asset.notes })
  }

  /**
   * 保存元数据。
   *
   * 请求表达的是**期望的完整状态**（空串清空、标签整体替换），因此把表单里的
   * 三个字段整组发出去。服务端才是长度的权威：这里的 maxLength 只是省一次
   * 往返，超限时仍按服务端返回的话呈现（见 docs/design/galaxy/asset-library.md）。
   */
  async function handleSaveMeta(): Promise<void> {
    if (editing === null) {
      return
    }
    // 表单校验没过时 validateFields 会拒绝。接住它：这不是失败，是**根本没发出去**
    // 的请求——正是客户端事件要区分出来的那一档（也顺手避免了未处理的拒绝）。
    let values
    try {
      values = await metaForm.validateFields()
    } catch {
      track({ surface: Surface.WEB_EDITOR, action: Action.ASSET_META_SAVE, result: Result.BLOCKED })
      return
    }
    setSavingMeta(true)
    setMetaFailure(null)
    try {
      await galaxyApi.updateAsset(
        projectId,
        editing.id,
        values.title ?? '',
        values.tags ?? [],
        values.notes ?? '',
      )
      setEditing(null)
      await onChanged()
    } catch (err) {
      setMetaFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setSavingMeta(false)
    }
  }

  /**
   * 按选中的文件声明的 MIME 主类型找该类别的上限。
   *
   * 类别由声明的 MIME 猜（服务端才是权威）；猜不出类别时返回 null，
   * 表示不在客户端早退，交由服务端拒。
   */
  function limitFor(file: File): AssetKindLimit | null {
    const kind = mediaKindOfDeclaredType(file.type)
    if (kind === null) {
      return null
    }
    return capabilities.assetLimits.find((limit) => limit.kind === kind) ?? null
  }

  async function handleFile(file: File): Promise<void> {
    const limit = limitFor(file)
    if (limit !== null && file.size > limit.maxBytes) {
      // 按声明的上限在本地早退：请求没发出去，服务端留痕里没有这次尝试。
      track({ surface: Surface.WEB_EDITOR, action: Action.ASSET_UPLOAD, result: Result.BLOCKED })
      setFailure({
        message: `该类别资产不能超过 ${describeBytes(limit.maxBytes)}`,
        traceId: null,
      })
      // 超限重试同一个文件没有意义，不给重试入口。
      setRetryFile(null)
      return
    }

    setBusy(true)
    setFailure(null)
    setRetryFile(null)
    try {
      // 摘要必须在提交前算好：它是公开区地址的键，服务端没有字节可以算它，
      // 且会在发布时核对（见 docs/design/galaxy/asset-library.md）。
      const digest = await sha256Hex(await file.arrayBuffer())
      const begin = await galaxyApi.beginAssetUpload(projectId, file.type, file.size)
      if (begin.upload === undefined) {
        throw new Error('服务端没有返回直传凭证')
      }
      // 类型由上传方声明（file.type）；不在白名单时把服务端的错误原样呈现。
      await directUpload(begin.upload, file, file.type)
      await galaxyApi.commitAssetUpload(projectId, begin.assetId, file.type, digest, file.name)
      await onChanged()
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      setRetryFile(file)
    } finally {
      setBusy(false)
    }
  }

  async function handleDelete(assetId: string): Promise<void> {
    setBusy(true)
    setFailure(null)
    try {
      await galaxyApi.deleteAsset(projectId, assetId)
      await onChanged()
    } catch (err) {
      // 资产被某个版本引用时服务端会拒绝，并在信息里指出是哪些版本。
      // 前端不复写这条判断，只把它原样呈现出来。
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusy(false)
    }
  }

  function referenceOf(asset: Asset): string {
    return `asset://${asset.id}`
  }

  async function handleCopy(asset: Asset): Promise<void> {
    try {
      await navigator.clipboard?.writeText(referenceOf(asset))
      setCopiedId(asset.id)
    } catch {
      // 剪贴板不可用（无权限、非安全上下文）时不做特殊处理：用户仍可手动选中
      // 标识。这里不弹一个只会让人困惑的错误。
      setCopiedId(null)
    }
  }

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

      {projectTags.length > 0 && (
        <Space size={8} wrap>
          <Typography.Text type="secondary">按标签筛选</Typography.Text>
          <Select
            mode="multiple"
            allowClear
            style={{ minWidth: 260 }}
            placeholder="同时带这些标签的才列出"
            value={[...filterTags]}
            onChange={onFilterChange}
            options={projectTags.map((tag) => ({ value: tag, label: tag }))}
            aria-label="按标签筛选资产"
          />
        </Space>
      )}

      {canWrite && (
        <Space orientation="vertical" size={4} style={{ width: '100%' }}>
          <Upload
            accept={ACCEPTED_TYPES}
            showUploadList={false}
            beforeUpload={(file) => {
              void handleFile(file)
              return false
            }}
          >
            <Button icon={<ImageUp size={16} />} loading={busy}>
              上传资产
            </Button>
          </Upload>
          <Typography.Text type="secondary">
            图片 / 视频 / 音频，上传时声明类型。正文里用{' '}
            <Typography.Text code>asset://&lt;资产标识&gt;</Typography.Text> 引用，点资产上的
            「复制引用」直接拿到这段文本。
          </Typography.Text>
          <Typography.Text type="secondary">
            编辑态用的是短时地址，未发布的资产<b>不得承载秘密</b>；它们在被发布前不对公开可见。
          </Typography.Text>
        </Space>
      )}

      {assets.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description={
            filterTags.length === 0 ? '资产库里还没有素材' : '没有同时带这些标签的素材'
          }
        />
      ) : (
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fill, minmax(240px, 1fr))',
            gap: 12,
          }}
        >
          {assets.map((asset) => (
            <AssetCard
              key={asset.id}
              asset={asset}
              reference={referenceOf(asset)}
              copied={copiedId === asset.id}
              canWrite={canWrite}
              borderColor={token.colorBorderSecondary}
              mediaBackground={token.colorFillQuaternary}
              onCopy={() => void handleCopy(asset)}
              onEdit={() => openMetaEditor(asset)}
              onDelete={() => void handleDelete(asset.id)}
            />
          ))}
        </div>
      )}

      <Modal
        title="编辑资产信息"
        open={editing !== null}
        onCancel={() => setEditing(null)}
        onOk={() => void handleSaveMeta()}
        okText="保存"
        confirmLoading={savingMeta}
        destroyOnHidden
      >
        {metaFailure !== null && (
          <Alert
            type="error"
            title={metaFailure.message}
            description={
              metaFailure.traceId !== null && (
                <Typography.Text type="secondary" copyable>
                  追踪 ID：{metaFailure.traceId}
                </Typography.Text>
              )
            }
            style={{ marginBottom: 12 }}
          />
        )}
        <Form form={metaForm} layout="vertical">
          <Form.Item
            name="title"
            label="展示标题"
            extra="留空则列表里显示原始文件名"
          >
            <Input placeholder="不给素材起名也行" maxLength={TITLE_MAX_RUNES} />
          </Form.Item>
          <Form.Item
            name="tags"
            label="标签"
            extra={`回车添加一个；服务端统一小写并去重，最多 ${MAX_TAGS} 个`}
          >
            <Select
              mode="tags"
              open={false}
              suffixIcon={null}
              tokenSeparators={[',', '，']}
              placeholder="输入后回车"
            />
          </Form.Item>
          <Form.Item
            name="notes"
            label="备注"
            extra="只给你自己看：不进发布产物，也不出现在公开页面上"
          >
            <Input.TextArea rows={4} maxLength={NOTES_MAX_RUNES} showCount />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}

interface AssetCardProps {
  asset: Asset
  reference: string
  copied: boolean
  canWrite: boolean
  borderColor: string
  mediaBackground: string
  onCopy: () => void
  onEdit: () => void
  onDelete: () => void
}

/**
 * 一个资产。上面是媒体（点一下放大查看），下面是它的元数据与两个动作。
 *
 * 元数据（类型、大小、时间）与短时地址的说明都挤在卡片里，是因为这一批东西
 * 本来就该一起看：换个文件名要能立刻对上是哪一张图。
 *
 * **主标签是展示标题，没有才回退到文件名**（见 docs/design/galaxy/asset-library.md）：
 * 文件名常是工具生成的乱码，而标题是给人认的。两者都有时把文件名放在下面一行，
 * 免得"这张图是从哪个文件来的"变成一个查不到的问题。
 */
function AssetCard({
  asset,
  reference,
  copied,
  canWrite,
  borderColor,
  mediaBackground,
  onCopy,
  onEdit,
  onDelete,
}: AssetCardProps): React.ReactNode {
  const named = asset.title !== ''
  const label = named ? asset.title : asset.filename === '' ? '(未命名)' : asset.filename
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        border: `1px solid ${borderColor}`,
        borderRadius: 6,
        overflow: 'hidden',
      }}
    >
      <div
        style={{
          height: MEDIA_HEIGHT,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: mediaBackground,
          overflow: 'hidden',
        }}
      >
        <AssetMedia asset={asset} label={label} />
      </div>
      <Space orientation="vertical" size={4} style={{ padding: 8, width: '100%' }}>
        <Typography.Text ellipsis={{ tooltip: label }}>{label}</Typography.Text>
        {named && (
          <Typography.Text
            type="secondary"
            style={{ fontSize: 12 }}
            ellipsis={{ tooltip: asset.filename }}
          >
            文件名：{asset.filename === '' ? '(未命名)' : asset.filename}
          </Typography.Text>
        )}
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {asset.mediaType} · {describeBytes(Number(asset.sizeBytes))}
        </Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {formatTime(asset.uploadedAt)}
        </Typography.Text>
        {asset.tags.length > 0 && (
          <Space size={4} wrap>
            {asset.tags.map((tag) => (
              <Tag key={tag} style={{ marginInlineEnd: 0 }}>
                {tag}
              </Tag>
            ))}
          </Space>
        )}
        {asset.notes !== '' && (
          // 截断展示，鼠标停上去看全文：备注是给人看的说明，卡片里放不下。
          <Typography.Text
            type="secondary"
            style={{ fontSize: 12, whiteSpace: 'pre-wrap' }}
            ellipsis={{ tooltip: asset.notes }}
          >
            {asset.notes}
          </Typography.Text>
        )}
        <Typography.Text code style={{ fontSize: 12, wordBreak: 'break-all' }}>
          {reference}
        </Typography.Text>
        <Space size={4} wrap>
          <Button type="link" size="small" icon={<Copy size={14} />} onClick={onCopy}>
            复制引用
          </Button>
          {canWrite && (
            <Button type="link" size="small" icon={<Pencil size={14} />} onClick={onEdit}>
              编辑
            </Button>
          )}
          {canWrite && (
            <Popconfirm
              title="删除这个资产？"
              description="被任一版本引用时会被拒绝，需先删掉引用它的版本。"
              okText="删除"
              okButtonProps={{ danger: true }}
              onConfirm={onDelete}
            >
              <Button type="link" size="small" danger icon={<Trash2 size={14} />}>
                删除
              </Button>
            </Popconfirm>
          )}
        </Space>
        {copied && (
          <Typography.Text type="success" style={{ fontSize: 12 }}>
            已复制引用
          </Typography.Text>
        )}
      </Space>
    </div>
  )
}

/**
 * 资产的查看。
 *
 * 图片走 antd 的 `Image`：点一下放大到原图（这是"查看"，不是缩略图——本模块
 * 不做转码，放大的就是原样的字节）。视频与音频用各自的控件播放。
 * 服务端记录的类型前缀决定用哪一种；取不到地址时原样说明，不装作有。
 */
function AssetMedia({ asset, label }: { asset: Asset; label: string }): React.ReactNode {
  if (asset.url === '') {
    return <Typography.Text type="secondary">无预览</Typography.Text>
  }
  if (asset.mediaType.startsWith('image/')) {
    return (
      <Image
        src={asset.url}
        alt={label}
        style={{ maxWidth: '100%', maxHeight: MEDIA_HEIGHT - 16, objectFit: 'contain' }}
      />
    )
  }
  if (asset.mediaType.startsWith('video/')) {
    return <video src={asset.url} controls style={{ maxWidth: '100%', maxHeight: MEDIA_HEIGHT }} />
  }
  if (asset.mediaType.startsWith('audio/')) {
    return <audio src={asset.url} controls style={{ width: '100%' }} />
  }
  return null
}
