import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, Button, Flex, Input, Modal, Segmented, Skeleton, Space, Typography } from 'antd'
import { ExternalLink, Eye, FileCode, RefreshCw, Save } from 'lucide-react'
import { useParams } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import { usePermission } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import type {
  Asset,
  Capabilities,
  Project,
  Version,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { useNarrowViewport } from '../../layouts/use-narrow-viewport'
import { MONOSPACE } from '../../theme'
import { AssetLibrary } from './AssetLibrary'
import { LifecycleStrip } from './LifecycleStrip'
import { formatTime } from './format-time'
import { PreviewFrame } from './PreviewFrame'
import {
  VALIDATION_PENDING,
  VALIDATION_STALE,
  type ValidationState,
} from './validation-state'
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

/** 预览与源码是两个模式，共用这一块面积（见 authoring.md 的"编辑页的形态"）。 */
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
 * **它以预览与状态为主体，不是一个编辑器页面**：创作路径在命令行（正文由本地工具
 * 或生成器写好送上来），这一页要回答的是"草稿现在渲染成什么样、能不能发布、
 * 下一步做什么"。因此主区铺满：预览与源码**共用这一块面积、切换着看**（默认预览），
 * 推进流程的动作在顶栏，资产与版本这一类"一批东西"从顶栏以弹层打开。
 * 要看渲染结果又想同时做别的事时，用「单独打开」把预览开成一个独立页面（PreviewPage）。
 * 见 docs/design/galaxy/authoring.md 的"编辑页的形态"。
 *
 * 路由已由 RequirePermission 保证 `galaxy.project.read`；页面内部按更细的权限码
 * 裁剪写操作（保存草稿/存版本用 write、发布用 publish、资产用 asset.*）。
 *
 * 校验**自动产生**：打开页面与每次保存草稿之后都调一次服务端的 ValidateContent，
 * 结论呈现在状态条与概览里。前端不复写引用解析——"这段正文能不能发布"只有
 * 服务端一个实现入口，两端各写一份的表现是"编辑器说没问题、发布说不行"。
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
  const [draft, setDraft] = useState('')
  const [draftSavedAt, setDraftSavedAt] = useState('')
  const [versions, setVersions] = useState<Version[]>([])
  const [assets, setAssets] = useState<Asset[]>([])

  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<failure | null>(null)

  const [contentBusy, setContentBusy] = useState(false)
  const [publishBusy, setPublishBusy] = useState(false)
  const [previewBusy, setPreviewBusy] = useState(false)
  const [validation, setValidation] = useState<ValidationState>(VALIDATION_PENDING)
  const [loadedSeq, setLoadedSeq] = useState<number | null>(null)
  const [mode, setMode] = useState<StageMode>('preview')
  const [panel, setPanel] = useState<Panel | null>(null)

  // 校验的竞态闸门：保存草稿与首次加载都会触发校验，序号让先发后到的响应作废，
  // 否则状态条上会停在一次过期的结论上。
  const validateSeq = useRef(0)

  const validate = useCallback(
    async (content: string): Promise<void> => {
      if (projectId === undefined) {
        return
      }
      const seq = ++validateSeq.current
      setValidation(VALIDATION_PENDING)
      try {
        const response = await galaxyApi.validateContent(projectId, content)
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
        // 「校验没跑成」不等于「正文有问题」：只标成未完成，不冒充结论，
        // 也不把整页打成失败（预览、版本、资产照常可用）。
        setValidation({ status: 'failed', problems: [] })
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

  /**
   * 重新读取资产清单，即刷新预览里的短时地址。
   *
   * 编辑页长时间开着时，预览的图片会在地址过期后显示不出来——这正是这个按钮存在的
   * 理由（见 docs/design/galaxy/authoring.md 的「预览」）。
   *
   * 它不把错误直接抛出去：`loadAssets` 同时是资产库的回调，那条路径要自己呈现失败，
   * 这里的失败属于这一页。
   */
  async function refreshPreview(): Promise<void> {
    setPreviewBusy(true)
    setFailure(null)
    try {
      await loadAssets()
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setPreviewBusy(false)
    }
  }

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
      setProject(projectResponse.project ?? null)
      setCapabilities(capabilityResponse.capabilities ?? null)

      const [draftResponse, versionResponse] = await Promise.all([
        galaxyApi.getDraft(projectId),
        galaxyApi.listVersions(projectId),
      ])
      const content = draftResponse.draft?.content ?? ''
      setDraft(content)
      setDraftSavedAt(draftResponse.draft?.updatedAt ?? '')
      setVersions(versionResponse.versions)

      // 资产区只在"能力启用且持有读权限"时才请求：未配置私有桶时服务端拿不到
      // 地址，没权限时请求本身就会被拒——两者都不该让整页失败。
      if (capabilityResponse.capabilities?.assetUploadEnabled === true && canReadAssets) {
        const assetResponse = await galaxyApi.listAssets(projectId)
        setAssets(assetResponse.assets)
      } else {
        setAssets([])
      }

      // 打开页面就把"这份草稿能不能发布"问出来：用户到这一页本来就是来问这件事的。
      await validate(content)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setLoading(false)
    }
  }, [projectId, canReadAssets, validate])

  useEffect(() => {
    void load()
  }, [load])

  function handleDraftChange(value: string): void {
    setDraft(value)
    setLoadedSeq(null)
    // 上一次的结论描述的是改动之前的那份字节，继续显示它等于给出一个不成立的保证；
    // 保存草稿时会重新问一次服务端。
    setValidation(VALIDATION_STALE)
  }

  async function handleSaveDraft(): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setContentBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.saveDraft(projectId, draft)
      setDraftSavedAt(response.draft?.updatedAt ?? '')
      setLoadedSeq(null)
      await validate(draft)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setContentBusy(false)
    }
  }

  async function handleSaveVersion(): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setContentBusy(true)
    setFailure(null)
    try {
      // 保存版本快照的是**服务端的草稿**，所以先把编辑器里的内容落成草稿，
      // 再保存版本——否则版本会停留在上一次保存的草稿上。
      const draftResponse = await galaxyApi.saveDraft(projectId, draft)
      setDraftSavedAt(draftResponse.draft?.updatedAt ?? '')
      await galaxyApi.saveVersion(projectId)
      await reloadVersions()
      setLoadedSeq(null)
      await validate(draft)
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

  const topBar = (
    <WorkbenchTopBar
      project={project}
      versions={versions}
      canWrite={canWrite}
      canPublish={canPublish}
      publishEnabled={capabilities?.publishEnabled === true}
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

  const strip = (
    <LifecycleStrip
      validation={validation}
      versions={versions}
      project={project}
      publishEnabled={capabilities?.publishEnabled === true}
      canPublish={canPublish}
      publishBusy={publishBusy}
      onRetryValidate={() => void validate(draft)}
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
          onReadBack={(content, seq) => {
            // 读回一个版本的正文就切到源码并收起面板：不切的话，用户点了「读回」
            // 只看到预览没变，而正文其实已经换成了那一版。
            setDraft(content)
            setLoadedSeq(seq)
            setMode('source')
            setPanel(null)
            void validate(content)
          }}
          onChanged={reloadVersions}
        />
      </Modal>
    </>
  )

  const sourceState =
    loadedSeq !== null
      ? `已载入版本 #${loadedSeq}，尚未保存`
      : draftSavedAt === ''
        ? '草稿尚未保存'
        : `草稿保存于 ${formatTime(draftSavedAt)}`

  /**
   * 预览与源码共用的一块面积。
   *
   * 两个模式各自的动作跟着各自的模式走：预览侧是"再看一眼 / 拿去别处看"，
   * 源码侧是"把改动落下去"。放在同一行里会让当前不成立的动作一直亮着。
   */
  const stage = (
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
              disabled={!canReadAssets}
              onClick={() => void refreshPreview()}
            >
              刷新
            </Button>
          </Space>
        ) : (
          <Space wrap>
            {canWrite && (
              <Button
                type="primary"
                icon={<Save size={16} />}
                loading={contentBusy}
                onClick={() => void handleSaveDraft()}
              >
                保存草稿
              </Button>
            )}
            <Typography.Text type="secondary">{sourceState}</Typography.Text>
          </Space>
        )}
      </Flex>
      <div style={narrow ? { height: NARROW_STAGE_HEIGHT } : { flex: 1, minHeight: 0 }}>
        {mode === 'preview' ? (
          <PreviewFrame content={draft} assets={assets} height="100%" />
        ) : (
          <Input.TextArea
            value={draft}
            onChange={(event) => handleDraftChange(event.target.value)}
            spellCheck={false}
            disabled={!canWrite}
            placeholder="<!doctype html> 起手，写一份完整的 HTML 文档；素材用 asset://<资产标识> 引用"
            style={{
              height: '100%',
              resize: 'none',
              fontFamily: MONOSPACE,
              fontSize: 13,
            }}
          />
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
      {strip}
      {stage}
      {panels}
    </Flex>
  )
}
