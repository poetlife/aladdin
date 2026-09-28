import { useState } from 'react'
import { Alert, Button, Empty, List, Popconfirm, Space, Typography, Upload } from 'antd'
import { Copy, ImageUp, Trash2 } from 'lucide-react'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import type { Asset, AssetKindLimit } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { sha256Hex } from '../../upload/content-digest'
import { directUpload } from '../../upload/direct-upload'
import { describeBytes } from '../../format/bytes'
import { formatTime } from './format-time'
import { mediaKindOfDeclaredType } from './media-kind'

// 文件选择框的过滤（见 docs/design/galaxy/asset-library.md 的白名单）；它只影响
// 选择框里能看到什么，**不是校验**：真正的类型由上传方声明、由服务端与存储侧判定。
const ACCEPTED_TYPES =
  'image/png,image/jpeg,image/gif,image/webp,video/mp4,video/webm,audio/mpeg,audio/ogg,audio/wav'

interface AssetLibraryProps {
  projectId: string
  capabilities: {
    assetLimits: readonly AssetKindLimit[]
  }
  assets: readonly Asset[]
  /** 是否持有上传/删除权限。无权限时不渲染这两个入口。 */
  canWrite: boolean
  /** 上传或删除成功之后重新拉取资产清单（含新的短时地址）。 */
  onChanged: () => Promise<void>
}

interface failure {
  message: string
  traceId: string | null
}

/**
 * 工程资产库：上传、列出、复制引用、删除。
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
  canWrite,
  onChanged,
}: AssetLibraryProps): React.ReactNode {
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<failure | null>(null)
  // 最近一次失败的文件：留着它，用户点「重试」时能原样再来一次。上传失败
  // （网络中断、提交报"上传没有完成"）必须有一条可重试的提示，不许静默。
  const [retryFile, setRetryFile] = useState<File | null>(null)
  const [copiedId, setCopiedId] = useState<string | null>(null)

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

      {canWrite && (
        <Space orientation="vertical" size={8} style={{ width: '100%' }}>
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
            图片 / 视频 / 音频，上传时声明类型。正文里用 <Typography.Text code>asset://&lt;资产标识&gt;</Typography.Text>
            {' '}引用，点下面的「复制引用」直接拿到这段文本。
          </Typography.Text>
          <Typography.Text type="secondary">
            编辑态用的是短时地址，未发布的资产<b>不得承载秘密</b>；它们在被发布前不对公开可见。
          </Typography.Text>
        </Space>
      )}

      {assets.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="资产库里还没有素材" />
      ) : (
        <List<Asset>
          dataSource={[...assets]}
          rowKey="id"
          renderItem={(asset) => (
            <List.Item
              style={{ display: 'block' }}
              actions={[
                <Button
                  key="copy"
                  type="link"
                  icon={<Copy size={14} />}
                  onClick={() => void handleCopy(asset)}
                >
                  复制引用
                </Button>,
                ...(canWrite
                  ? [
                      <Popconfirm
                        key="delete"
                        title="删除这个资产？"
                        description="被任一版本引用时会被拒绝，需先删掉引用它的版本。"
                        okText="删除"
                        okButtonProps={{ danger: true }}
                        onConfirm={() => void handleDelete(asset.id)}
                      >
                        <Button type="link" danger icon={<Trash2 size={14} />}>
                          删除
                        </Button>
                      </Popconfirm>,
                    ]
                  : []),
              ]}
            >
              <Space size={16} align="start" wrap>
                <AssetPreview asset={asset} />
                <Space orientation="vertical" size={4}>
                  <Typography.Text style={{ wordBreak: 'break-all' }}>
                    {asset.filename === '' ? '(未命名)' : asset.filename}
                  </Typography.Text>
                  <Typography.Text type="secondary">
                    {asset.mediaType} · {describeBytes(Number(asset.sizeBytes))} ·{' '}
                    {formatTime(asset.uploadedAt)}
                  </Typography.Text>
                  <Typography.Text type="secondary" code style={{ wordBreak: 'break-all' }}>
                    {referenceOf(asset)}
                  </Typography.Text>
                  {copiedId === asset.id && (
                    <Typography.Text type="success">已复制引用</Typography.Text>
                  )}
                </Space>
              </Space>
            </List.Item>
          )}
        />
      )}
    </Space>
  )
}

/**
 * 资产的缩略/预览。
 *
 * 按服务端记录的 mediaType 前缀选择展示方式——**不做转码、不做缩略图**
 * （见 docs/design/galaxy/asset-library.md），原样取用字节。地址是短时地址，
 * 过期后重新读取资产清单即得到新的。
 */
function AssetPreview({ asset }: { asset: Asset }): React.ReactNode {
  if (asset.url === '') {
    return (
      <Typography.Text type="secondary" style={{ width: 96, textAlign: 'center' }}>
        无预览
      </Typography.Text>
    )
  }
  if (asset.mediaType.startsWith('image/')) {
    return <img src={asset.url} alt={asset.filename} style={{ maxWidth: 160, maxHeight: 96 }} />
  }
  if (asset.mediaType.startsWith('video/')) {
    return <video src={asset.url} controls style={{ maxWidth: 240, maxHeight: 96 }} />
  }
  if (asset.mediaType.startsWith('audio/')) {
    return <audio src={asset.url} controls style={{ maxWidth: 240 }} />
  }
  return null
}
