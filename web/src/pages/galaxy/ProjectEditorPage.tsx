import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, Button, Empty, Flex, Modal, Segmented, Select, Skeleton, Space, Tag, Typography } from 'antd'
import { ExternalLink, Eye, FileCode, RefreshCw } from 'lucide-react'
import { useParams } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import { usePermission } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import {
  SiteForm,
  type Asset,
  type Capabilities,
  type FileEntry,
  type Project,
  type Version,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { useNarrowViewport } from '../../layouts/use-narrow-viewport'
import { MONOSPACE } from '../../theme'
import { AssetLibrary } from './AssetLibrary'
import { LifecycleStrip } from './LifecycleStrip'
import { formatTime } from './format-time'
import { PreviewFrame } from './PreviewFrame'
import { VALIDATION_PENDING, type ValidationState } from './validation-state'
import { VersionList } from './VersionList'
import { WorkbenchTopBar } from './WorkbenchTopBar'

/** 预览/源码那块面积的下限与窄屏定高。 */
const STAGE_MIN_HEIGHT = 240
const NARROW_STAGE_HEIGHT = 360

/**
 * 弹层内容自己滚动。
 *
 * 不给上限时，内容比视口高会把**父页面**撑长、由外面那层滚——弹层跟着整页跑，
 * 标题栏与遮罩都跟着动。给内容区一个上限让它内部滚，弹层才是一个稳定的框。
 */
const PANEL_BODY_STYLE: React.CSSProperties = {
  maxHeight: 'calc(100dvh - 220px)',
  overflowY: 'auto',
}

/** 预览与源码是两个模式，共用这一块面积（见 authoring.md 的"这一页的形态"）。 */
type StageMode = 'preview' | 'source'

/** 顶栏能打开的两个集合。它们是弹层，不是页面上的常驻分区。 */
type Panel = 'assets' | 'versions'

interface failure {
  message: string
  traceId: string | null
}

/**
 * galaxy 工作台。
 *
 * **它以预览与状态为主体，且是只读的。** 内容的写入只有命令行一条路（push 表达
 * 整组的期望状态）：写入要么整组、要么单条，两者并存才会引出"网页上刚改的一句被
 * 一次 push 静默盖掉"这类只在两个入口之间发生的冲突；收成一条路径，那份冲突与它
 * 需要的基线校验一起不需要了（见 docs/design/galaxy/authoring.md）。
 *
 * 因此这一页只回答三件事：**草稿渲染成什么样、现在处于哪一步、把它推出去**。
 * 主区铺满——预览与源码共用这一块面积、切换着看（默认预览）；推进流程的动作在
 * 顶栏；资产与版本这一类"一批东西"从顶栏以弹层打开。要看渲染结果又想同时做别的事
 * 时，用「单独打开」把预览开成独立页面（PreviewPage）。
 *
 * 路由已由 RequirePermission 保证 `galaxy.project.read`；页面内部按更细的权限码
 * 裁剪动作（存版本用 write、发布用 publish、资产用 asset.*）。
 *
 * 校验**自动产生**：打开页面就调一次服务端的 ValidateDraft，结论呈现在状态条上。
 * 前端不复写引用解析——"这份草稿能不能发布"只有服务端一个实现入口，两端各写一份
 * 的表现是"提示说没问题、发布说不行"。
 */
export function ProjectEditorPage(): React.ReactNode {
  const { projectId } = useParams<{ projectId: string }>()
  const narrow = useNarrowViewport()

  const canWrite = usePermission(PermissionCodes.GalaxyProjectWrite)
  const canReadAssets = usePermission(PermissionCodes.GalaxyAssetRead)
  const canWriteAssets = usePermission(PermissionCodes.GalaxyAssetWrite)
  const canPublish = usePermission(PermissionCodes.GalaxyProjectPublish)

  const [project, setProject] = useState<Project | null>(null)
  const [capabilities, setCapabilities] = useState<Capabilities | null>(null)
  const [entries, setEntries] = useState<FileEntry[]>([])
  const [draftUpdatedAt, setDraftUpdatedAt] = useState('')
  const [versions, setVersions] = useState<Version[]>([])
  const [assets, setAssets] = useState<Asset[]>([])

  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<failure | null>(null)

  const [contentBusy, setContentBusy] = useState(false)
  const [publishBusy, setPublishBusy] = useState(false)
  const [previewBusy, setPreviewBusy] = useState(false)
  const [validation, setValidation] = useState<ValidationState>(VALIDATION_PENDING)
  const [mode, setMode] = useState<StageMode>('preview')
  const [panel, setPanel] = useState<Panel | null>(null)

  // 预览的那一页（`static` 形态逐页预览，`docs` 形态整站拼成一份、忽略它）。
  const [previewPath, setPreviewPath] = useState('')
  const [previewHtml, setPreviewHtml] = useState('')
  // 预览失败与"这一页失败"是两件事：渲染取不到内容（比如这个部署没配桶）时，
  // 状态条、版本与资产面板照常可用——把一次取不到渲染结果渲染成一片失败，会让
  // 用户以为自己的工程坏了（见 docs/design/galaxy/authoring.md）。
  const [previewError, setPreviewError] = useState<string | null>(null)
  // 源码视图里选中的那一份文件与它的原文。
  const [selectedPath, setSelectedPath] = useState('')
  const [sourceText, setSourceText] = useState('')
  const [sourceBusy, setSourceBusy] = useState(false)

  // 校验的竞态闸门：重新加载与重新校验都发请求，序号让先发后到的响应作废，
  // 否则状态条上会停在一次过期的结论上。
  const validateSeq = useRef(0)

  const validate = useCallback(async (): Promise<void> => {
    if (projectId === undefined) {
      return
    }
    const seq = ++validateSeq.current
    setValidation(VALIDATION_PENDING)
    try {
      const response = await galaxyApi.validateDraft(projectId)
      if (seq !== validateSeq.current) {
        return
      }
      setValidation({
        status: response.problems.length === 0 ? 'ok' : 'problems',
        problems: response.problems,
      })
    } catch {
      if (seq !== validateSeq.current) {
        return
      }
      // 「校验没跑成」不等于「内容有问题」：只标成未完成，不冒充结论，
      // 也不把整页打成失败（预览、版本、资产照常可用）。
      setValidation({ status: 'failed', problems: [] })
    }
  }, [projectId])

  /**
   * 渲染一次预览。
   *
   * 渲染在服务端（与发布共用同一段实现），因此这里只负责把结果放进沙箱。
   * 它同时刷新了内容里的短时资产地址——这正是「刷新」按钮存在的理由。
   */
  const renderPreview = useCallback(
    async (path: string): Promise<void> => {
      if (projectId === undefined) {
        return
      }
      try {
        const response = await galaxyApi.previewDraft(projectId, path)
        setPreviewHtml(response.html)
        setPreviewError(null)
      } catch (err) {
        // 预览这一块自己呈现失败，不把整页打成失败。
        setPreviewHtml('')
        setPreviewError(messageOf(err))
      }
    },
    [projectId],
  )

  const loadAssets = useCallback(async (): Promise<void> => {
    if (projectId === undefined || !canReadAssets) {
      return
    }
    const response = await galaxyApi.listAssets(projectId)
    setAssets(response.assets)
  }, [projectId, canReadAssets])

  const reloadVersions = useCallback(async (): Promise<void> => {
    if (projectId === undefined) {
      return
    }
    const response = await galaxyApi.listVersions(projectId)
    setVersions(response.versions)
  }, [projectId])

  const load = useCallback(async (): Promise<void> => {
    if (projectId === undefined) {
      return
    }
    setLoading(true)
    setFailure(null)
    try {
      const [projectResponse, capabilityResponse] = await Promise.all([
        galaxyApi.getProject(projectId),
        galaxyApi.getCapabilities(),
      ])
      const loadedProject = projectResponse.project ?? null
      setProject(loadedProject)
      setCapabilities(capabilityResponse.capabilities ?? null)

      const [draftResponse, versionResponse] = await Promise.all([
        galaxyApi.getDraft(projectId),
        galaxyApi.listVersions(projectId),
      ])
      const loadedEntries = draftResponse.draft?.entries ?? []
      setEntries(loadedEntries)
      setDraftUpdatedAt(draftResponse.draft?.updatedAt ?? '')
      setVersions(versionResponse.versions)
      // 默认落在入口文件与它的同目录首项上：那是"打开就看到内容"的位置。
      setPreviewPath(defaultPreviewPath(loadedProject, loadedEntries))
      setSelectedPath(loadedEntries[0]?.path ?? '')

      // 资产区只在"能力启用且持有读权限"时才请求：未配置私有桶时服务端拿不到
      // 地址，没权限时请求本身就会被拒——两者都不该让整页失败。
      if (capabilityResponse.capabilities?.assetUploadEnabled === true && canReadAssets) {
        const assetResponse = await galaxyApi.listAssets(projectId)
        setAssets(assetResponse.assets)
      } else {
        setAssets([])
      }

      // 打开页面就把"这份草稿能不能发布"问出来：用户到这一页本来就是来问这件事的。
      // **桶是内容的前提**：没配置桶时字节没有地方放，也就不存在草稿与版本——
      // 那时不去问校验与预览，改由下面渲染一句说明（见 spec 的"未配置时降级正确"）。
      if (capabilityResponse.capabilities?.assetUploadEnabled === true) {
        await validate()
        await renderPreview(defaultPreviewPath(loadedProject, loadedEntries))
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setLoading(false)
    }
  }, [projectId, canReadAssets, validate, renderPreview])

  useEffect(() => {
    void load()
  }, [load])

  /** 重新渲染预览，即刷新内容里的短时资产地址。 */
  async function handleRefreshPreview(): Promise<void> {
    setPreviewBusy(true)
    setFailure(null)
    try {
      await loadAssets()
      await renderPreview(previewPath)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setPreviewBusy(false)
    }
  }

  /** 读一份文件的原文：客户端按短时地址**直连**取，服务端不代理字节。 */
  async function handleSelectEntry(entry: FileEntry): Promise<void> {
    setSelectedPath(entry.path)
    setSourceText('')
    if (entry.source.case !== 'digest') {
      return
    }
    setSourceBusy(true)
    setFailure(null)
    try {
      const response = await fetch(entry.url)
      if (!response.ok) {
        throw new Error(`读取失败：HTTP ${response.status}`)
      }
      setSourceText(await response.text())
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: null })
    } finally {
      setSourceBusy(false)
    }
  }

  async function handleSaveVersion(): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setContentBusy(true)
    setFailure(null)
    try {
      // 保存版本冻结的是**服务端的草稿清单**。网页端不改内容，因此这里不需要
      // 先保存草稿——草稿正是命令行刚 push 上来的那一份。
      await galaxyApi.saveVersion(projectId)
      await reloadVersions()
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setContentBusy(false)
    }
  }

  async function handlePublish(versionId: string): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setPublishBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.publish(projectId, versionId)
      if (response.project !== undefined) {
        setProject(response.project)
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setPublishBusy(false)
    }
  }

  async function handleUnpublish(): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setPublishBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.unpublish(projectId)
      if (response.project !== undefined) {
        setProject(response.project)
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setPublishBusy(false)
    }
  }

  if (loading && project === null) {
    return <Skeleton active paragraph={{ rows: 8 }} />
  }

  if (project === null) {
    return (
      <Alert
        type="error"
        showIcon
        title={failure?.message ?? '读取工程失败'}
        description={
          failure !== null &&
          failure.traceId !== null && (
            <Typography.Text type="secondary" copyable>
              追踪 ID：{failure.traceId}
            </Typography.Text>
          )
        }
        action={<Button onClick={() => void load()}>重试</Button>}
      />
    )
  }

  const isDocs = project.form === SiteForm.DOCS
  // **桶是内容的前提**：没配置对象存储时，内容（草稿与版本）整体不可用，
  // 只有工程元数据可写。此时不渲染内容相关的入口与结论，改给一句说明。
  const contentEnabled = capabilities?.assetUploadEnabled === true
  const textEntries = entries.filter((entry) => entry.source.case === 'digest')
  const selectedEntry = entries.find((entry) => entry.path === selectedPath)
  const selectedAsset =
    selectedEntry?.source.case === 'assetId'
      ? assets.find((asset) => asset.id === selectedEntry.source.value)
      : undefined

  const topBar = (
    <WorkbenchTopBar
      project={project}
      versions={versions}
      canWrite={canWrite}
      canPublish={canPublish}
      publishEnabled={capabilities?.publishEnabled === true}
      contentEnabled={contentEnabled}
      assetPanelEnabled={capabilities?.assetUploadEnabled === true && canReadAssets}
      versionBusy={contentBusy}
      publishBusy={publishBusy}
      onOpenAssets={() => setPanel('assets')}
      onOpenVersions={() => setPanel('versions')}
      onSaveVersion={() => void handleSaveVersion()}
      onPublish={(versionId) => void handlePublish(versionId)}
      onProjectChange={setProject}
    />
  )

  // **没配置桶时不渲染内容相关的入口**，改给一句说明：内容（草稿、版本与产物）
  // 的字节没有地方放，这一页就只有工程信息可看。渲染一个点了报错的控件不是
  // "降级正确"（见 docs/design/galaxy/asset-library.md 的"未配置时"）。
  const contentUnavailable = (
    <Alert
      type="info"
      showIcon
      title="这个部署没有配置对象存储"
      description="内容的字节没有地方放，因此草稿、版本、预览与发布都不可用。工程信息与形态照常可读写。"
    />
  )

  const strip = contentEnabled && (
    <LifecycleStrip
      validation={validation}
      versions={versions}
      project={project}
      publishEnabled={capabilities?.publishEnabled === true}
      canPublish={canPublish}
      publishBusy={publishBusy}
      onRetryValidate={() => void validate()}
      onUnpublish={() => void handleUnpublish()}
    />
  )

  /**
   * 顶栏打开的两个集合：资产与版本。
   *
   * 它们是弹层而不是常驻分区——一批东西与主区并排，会让主区**长期**窄掉一截，
   * 换来的却是一个多数时候不看的列表（见 docs/design/galaxy/authoring.md 的
   * "主区铺满，集合进弹层"）。
   *
   * 关掉即卸载（`destroyOnHidden`）：否则上一次的失败提示与"已复制引用"会留到下次打开。
   */
  const panels = (
    <>
      <Modal
        title="资产"
        open={panel === 'assets'}
        onCancel={() => setPanel(null)}
        footer={null}
        width={860}
        destroyOnHidden
        styles={{ body: PANEL_BODY_STYLE }}
      >
        <AssetLibrary
          projectId={project.id}
          capabilities={{ assetLimits: capabilities?.assetLimits ?? [] }}
          assets={assets}
          canWrite={canWriteAssets}
          onChanged={loadAssets}
        />
      </Modal>

      <Modal
        title="版本"
        open={panel === 'versions'}
        onCancel={() => setPanel(null)}
        footer={null}
        width={640}
        destroyOnHidden
        styles={{ body: PANEL_BODY_STYLE }}
      >
        <VersionList
          projectId={project.id}
          versions={versions}
          canWrite={canWrite}
          onChanged={reloadVersions}
        />
      </Modal>
    </>
  )

  const sourceState =
    draftUpdatedAt === '' ? '草稿还是空的' : `草稿更新于 ${formatTime(draftUpdatedAt)}`

  /**
   * 预览与源码共用的一块面积。
   *
   * 两个模式各自的动作跟着各自的模式走：预览侧是"换一页 / 拿去别处看 / 刷新地址"，
   * 源码侧只是"这是哪一份、它写了什么"。放在同一行里会让当前不成立的动作一直亮着。
   */
  const stage = contentEnabled && (
    <Flex
      vertical
      gap={8}
      style={narrow ? undefined : { flex: 1, minWidth: 0, minHeight: STAGE_MIN_HEIGHT }}
    >
      <Flex align="center" justify="space-between" gap={12} wrap>
        <Segmented<StageMode>
          value={mode}
          onChange={setMode}
          options={[
            { value: 'preview', label: '预览', icon: <Eye size={14} /> },
            { value: 'source', label: '源码', icon: <FileCode size={14} /> },
          ]}
        />
        {mode === 'preview' ? (
          <Space wrap>
            {!isDocs && textEntries.length > 0 && (
              <Select
                size="small"
                value={previewPath}
                style={{ minWidth: 180 }}
                onChange={(value: string) => {
                  setPreviewPath(value)
                  void renderPreview(value)
                }}
                options={textEntries.map((entry) => ({ value: entry.path, label: entry.path }))}
              />
            )}
            <Button
              href={`/galaxy/${project.id}/preview`}
              target="_blank"
              rel="noopener"
              icon={<ExternalLink size={16} />}
            >
              单独打开
            </Button>
            <Button
              icon={<RefreshCw size={16} />}
              loading={previewBusy}
              onClick={() => void handleRefreshPreview()}
            >
              刷新
            </Button>
          </Space>
        ) : (
          <Typography.Text type="secondary">{sourceState}</Typography.Text>
        )}
      </Flex>
      <div style={narrow ? { height: NARROW_STAGE_HEIGHT } : { flex: 1, minHeight: 0 }}>
        {mode === 'preview' ? (
          previewError !== null ? (
            <Alert
              type="warning"
              showIcon
              title="预览暂时渲染不出来"
              description={previewError}
              action={
                <Button size="small" onClick={() => void handleRefreshPreview()}>
                  重试
                </Button>
              }
              style={{ height: '100%', overflow: 'auto' }}
            />
          ) : (
            <PreviewFrame html={previewHtml} height="100%" />
          )
        ) : (
          <Flex gap={8} style={{ height: '100%', minHeight: 0 }}>
            {/* 源码视图是**文件列表 + 选中的那一份**，不是"一份正文"：内容本来
                就是一组具名文件，把它伪装成一份文本会立刻引出"我改的到底是哪一份"
                这个无法回答的问题。 */}
            <div
              style={{
                width: 220,
                overflowY: 'auto',
                border: '1px solid rgba(128,128,128,0.25)',
                borderRadius: 6,
                padding: 4,
                flexShrink: 0,
              }}
            >
              {entries.length === 0 ? (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  草稿还是空的。用命令行 push 一组文件上来。
                </Typography.Text>
              ) : (
                entries.map((entry) => (
                  <Flex
                    key={entry.path}
                    align="center"
                    justify="space-between"
                    gap={4}
                    onClick={() => void handleSelectEntry(entry)}
                    style={{
                      cursor: 'pointer',
                      padding: '4px 6px',
                      borderRadius: 4,
                      background: entry.path === selectedPath ? 'rgba(128,128,128,0.16)' : undefined,
                    }}
                  >
                    <Typography.Text style={{ fontSize: 12 }} ellipsis>
                      {entry.path}
                    </Typography.Text>
                    {entry.source.case === 'assetId' && (
                      <Tag style={{ marginInlineEnd: 0 }} color="blue">
                        资产
                      </Tag>
                    )}
                  </Flex>
                ))
              )}
            </div>
            <div
              style={{
                flex: 1,
                minWidth: 0,
                overflow: 'auto',
                border: '1px solid rgba(128,128,128,0.25)',
                borderRadius: 6,
                padding: 8,
              }}
            >
              {selectedEntry === undefined ? (
                <Empty description="选一份文件看它写了什么" image={Empty.PRESENTED_IMAGE_SIMPLE} />
              ) : selectedEntry.source.case === 'assetId' ? (
                // 资产是一份二进制，源码视图**不把它当文本读**——它只回答"这一份
                // 是什么"。内容看预览，要换去处看资产面板里的地址。
                <Space direction="vertical" size={4}>
                  <Typography.Text type="secondary">
                    这一份是资产（{selectedAsset?.mediaType ?? '类型未知'}）。
                  </Typography.Text>
                  {selectedAsset !== undefined && selectedAsset.url !== '' && (
                    <Typography.Link href={selectedAsset.url} target="_blank" rel="noopener noreferrer">
                      在新标签页打开
                    </Typography.Link>
                  )}
                </Space>
              ) : sourceBusy ? (
                <Skeleton active paragraph={{ rows: 6 }} />
              ) : (
                // **只读**：网页端不改内容（见 docs/design/galaxy/authoring.md）。
                <pre
                  style={{
                    margin: 0,
                    fontFamily: MONOSPACE,
                    fontSize: 13,
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-all',
                  }}
                >
                  {sourceText}
                </pre>
              )}
            </div>
          </Flex>
        )}
      </div>
    </Flex>
  )

  const failureAlert = failure !== null && (
    <Alert
      type="error"
      showIcon
      closable
      onClose={() => setFailure(null)}
      title={failure.message}
      description={
        failure.traceId !== null && (
          <Typography.Text type="secondary" copyable>
            追踪 ID：{failure.traceId}
          </Typography.Text>
        )
      }
    />
  )

  if (narrow) {
    // 窄屏不走分栏：并排的两栏在手机上各自只剩一条缝，而且并排要求两栏都撑满
    // 可用高度，这是 `wrap` 做不到的（见 docs/design/web/responsive.md）。
    return (
      <Flex vertical gap={12}>
        {failureAlert}
        {topBar}
        {!contentEnabled && contentUnavailable}
        {strip}
        {stage}
        {panels}
      </Flex>
    )
  }

  return (
    <Flex vertical gap={12} style={{ height: '100%', minHeight: 0 }}>
      {failureAlert}
      {topBar}
      {!contentEnabled && contentUnavailable}
      {strip}
      {stage}
      {panels}
    </Flex>
  )
}

/**
 * 默认预览哪一页：`static` 取入口页（`index.html`），没有就取第一份文本。
 * `docs` 的预览是整站拼成一份，路径无意义。
 */
function defaultPreviewPath(project: Project | null, entries: readonly FileEntry[]): string {
  if (project?.form === SiteForm.DOCS) {
    return ''
  }
  const entryPath = project?.form === SiteForm.STATIC ? 'index.html' : ''
  if (entryPath !== '' && entries.some((entry) => entry.path === entryPath)) {
    return entryPath
  }
  return entries.find((entry) => entry.source.case === 'digest')?.path ?? ''
}
