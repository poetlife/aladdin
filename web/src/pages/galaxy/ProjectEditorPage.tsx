import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, Button, Empty, Flex, Segmented, Select, Skeleton, Space, Typography } from 'antd'
import { ExternalLink, Eye, FileCode, RefreshCw } from 'lucide-react'
import { useParams } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { captureTrace, traceIdForAction, type TraceCapture } from '../../api/call-trace'
import { messageOf, traceIdOf } from '../../api/errors'
import { usePermission } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import {
  ContentSlot,
  type Asset,
  type Capabilities,
  type FileEntry,
  type Project,
  type Version,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { useNarrowViewport } from '../../layouts/use-narrow-viewport'
import { track } from '../../telemetry/track'
import { AppModal } from '../../ui/AppModal'
import { useWatch } from '../../watch/use-watch'
import { projectTopic } from '../../watch/topics'
import { AssetLibrary } from './AssetLibrary'
import { LifecycleStrip } from './LifecycleStrip'
import { formatTime } from './format-time'
import { SandboxFrame } from './SandboxFrame'
import { SourceView } from './SourceView'
import { VALIDATION_PENDING, type ValidationState } from './validation-state'
import { VersionList } from './VersionList'
import { WorkbenchTopBar } from './WorkbenchTopBar'

/**
 * 预览/源码那块面积的高度下限。
 *
 * **两态的差别只在下限**：上限都由"视口剩下的高度"给（宽窄都吃满，底部不留空），
 * 视口比下限还矮时整页滚动。窄屏的下限更高，因为它没有并排要照顾，而手机上一屏
 * 本来就矮——360 是"一屏内还看得出这一页长什么样"的取值。
 */
const STAGE_MIN_HEIGHT = 240
const NARROW_STAGE_MIN_HEIGHT = 360

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
 * 校验**自动产生**：打开页面就调一次服务端的 ValidateDraft；**这一页订阅了它正在
 * 看的那个工程的主题**（见 docs/design/events/README.md），别处（命令行、另一个
 * 标签页）改完就会推一条事件过来，随即重拉草稿、版本与预览并再问一次校验——状态条
 * 不会停在别的入口改动之前的结论上。**页面在后台不另外照顾**：隐藏期间不退订，事件
 * 照常交付；连接真的死了（冻结标签页只是其中一种成因）由通道按心跳判活、重连，而
 * 重连就会带来重拉（见 docs/design/events/README.md 的"页面隐藏时"）。前端不复写
 * 引用解析——"这份草稿能不能发布"只有服务端一个实现入口，两端各写一份的表现是
 * "提示说没问题、发布说不行"。
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
  // 当前看哪个内容槽。**一个工程可以两个槽都有，而这一页一次只看一个**：草稿、
  // 版本、状态条与预览都属于它（见 docs/design/galaxy/authoring.md）。槽只增不删，
  // 因此这里的切换只是"看哪一块"，不是改工程。
  const [slot, setSlot] = useState<ContentSlot>(ContentSlot.UNSPECIFIED)
  const [entries, setEntries] = useState<FileEntry[]>([])
  const [draftUpdatedAt, setDraftUpdatedAt] = useState('')
  const [versions, setVersions] = useState<Version[]>([])
  const [assets, setAssets] = useState<Asset[]>([])
  // 资产的标签候选：**整个工程**已有的标签，由服务端下发。它不随当前筛选收窄，
  // 否则筛一次之后候选就只剩下筛出来的那几个（见 ListAssets 的说明）。
  const [assetTags, setAssetTags] = useState<string[]>([])
  // 当前选中的标签筛选。筛选在**服务端**做，因此重拉时要把它带上——别处推来的
  // 事件重拉、刷新预览都带着它，用户视角里的"我筛着的那一批"不该被静默换掉。
  const [assetFilter, setAssetFilter] = useState<string[]>([])

  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<failure | null>(null)

  const [contentBusy, setContentBusy] = useState(false)
  const [publishBusy, setPublishBusy] = useState(false)
  const [previewBusy, setPreviewBusy] = useState(false)
  const [validation, setValidation] = useState<ValidationState>(VALIDATION_PENDING)
  const [mode, setMode] = useState<StageMode>('preview')
  const [panel, setPanel] = useState<Panel | null>(null)

  // 预览的那一页。地址由服务端给出（带短时凭证），前端不拼也不塞字节。
  const [previewPath, setPreviewPath] = useState('')
  const [previewUrl, setPreviewUrl] = useState('')
  // 预览失败与"这一页失败"是两件事：渲染取不到内容（比如这个部署没配桶）时，
  // 状态条、版本与资产面板照常可用——把一次取不到渲染结果渲染成一片失败，会让
  // 用户以为自己的工程坏了（见 docs/design/galaxy/authoring.md）。
  const [previewError, setPreviewError] = useState<string | null>(null)
  // 源码视图里选中的那一份文件与它的原文。
  const [selectedPath, setSelectedPath] = useState('')
  const [sourceText, setSourceText] = useState('')
  const [sourceBusy, setSourceBusy] = useState(false)

  // 校验、预览、回到前台这三路都会发请求。序号让先发后到的响应作废，
  // 否则状态条或预览会停在一次过期的结论上。
  const validateSeq = useRef(0)
  const previewSeq = useRef(0)
  const refreshSeq = useRef(0)
  const sourceSeq = useRef(0)

  // 当前看哪个槽，以及它的发布状态。**这一页的一切都挂在 activeSlot 上**：
  // 草稿、版本、校验结论、预览与状态条说的都是它。
  const activeSlot = pickSlot(project, slot)
  const activeSlotState = project?.slots.find((candidate) => candidate.slot === activeSlot)

  const validate = useCallback(async (target: ContentSlot): Promise<void> => {
    if (projectId === undefined) {
      return
    }
    const seq = ++validateSeq.current
    setValidation(VALIDATION_PENDING)
    try {
      const response = await galaxyApi.validateDraft(projectId, target)
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
   * 取一次预览地址。
   *
   * 服务端签发一条短时凭证并把地址拼好（见 docs/design/galaxy/site-model.md 的
   * "预览"），这里只把它交给沙箱 iframe。**重新取一次就是刷新**——凭证与内容里的
   * 短时资产地址都会换新，这正是「刷新」按钮存在的理由。
   *
   * 手头那一份路径在草稿里已经不存在时（命令行刚 push 过），服务端会退回入口；
   * 返回空地址表示草稿里还没有可预览的入口，那是空态而不是失败。
   */
  const renderPreview = useCallback(
    async (target: ContentSlot, path: string): Promise<void> => {
      if (projectId === undefined) {
        return
      }
      const seq = ++previewSeq.current
      try {
        const response = await galaxyApi.previewDraft(projectId, target, path)
        if (seq !== previewSeq.current) {
          return
        }
        setPreviewUrl(response.url)
        setPreviewError(null)
      } catch (err) {
        if (seq !== previewSeq.current) {
          return
        }
        // 预览这一块自己呈现失败，不把整页打成失败。
        setPreviewUrl('')
        setPreviewError(messageOf(err))
      }
    },
    [projectId],
  )

  /**
   * 读一份文件的原文：客户端按短时地址**直连**取，服务端不代理字节。
   *
   * `keepPrevious` 用于回到前台时重读同一份：先清空会让源码区闪一下空。
   */
  const readEntryText = useCallback(async (entry: FileEntry, keepPrevious = false): Promise<void> => {
    const seq = ++sourceSeq.current
    setSelectedPath(entry.path)
    if (!keepPrevious) {
      setSourceText('')
    }
    if (entry.source.case !== 'digest') {
      setSourceText('')
      return
    }
    setSourceBusy(true)
    setFailure(null)
    try {
      const response = await fetch(entry.url)
      if (seq !== sourceSeq.current) {
        return
      }
      if (!response.ok) {
        throw new Error(`读取失败：HTTP ${response.status}`)
      }
      setSourceText(await response.text())
    } catch (err) {
      if (seq !== sourceSeq.current) {
        return
      }
      setFailure({ message: messageOf(err), traceId: null })
    } finally {
      if (seq === sourceSeq.current) {
        setSourceBusy(false)
      }
    }
  }, [])

  // tagFilter 由调用方给出，而不是从状态里读：这个回调会被几个不同时机调用
  // （事件推来的重拉、刷新预览、面板里的动作），而"当前筛的是哪一批"必须由
  // 调用时机上那一个确定的值说了算。
  const loadAssets = useCallback(async (tagFilter: string[]): Promise<void> => {
    if (projectId === undefined || !canReadAssets) {
      return
    }
    const response = await galaxyApi.listAssets(projectId, tagFilter)
    setAssets(response.assets)
    setAssetTags(response.projectTags)
  }, [projectId, canReadAssets])

  /** 改资产筛选：筛选在服务端做，因此改完立刻按新的标签重拉一次。 */
  function handleAssetFilterChange(tags: string[]): void {
    setAssetFilter(tags)
    void loadAssets(tags)
  }

  const reloadVersions = useCallback(async (target: ContentSlot): Promise<void> => {
    if (projectId === undefined) {
      return
    }
    const response = await galaxyApi.listVersions(projectId, target)
    setVersions(response.versions)
  }, [projectId])

  const load = useCallback(async (): Promise<void> => {
    if (projectId === undefined) {
      return
    }
    setLoading(true)
    setFailure(null)
    // 打开工作台这一条事件伴随的不止一次 RPC（工程、能力、草稿、版本、资产、校验、
    // 预览各一次），而事件只带得了一个 trace_id，因此按"这次动作的**第一次**调用"
    // 取——它是这次打开的必要条件，且不随"有没有配置资产桶/发布域"改变条数
    // （见 ../../api/call-trace 的 traceIdForAction）。
    const trace = captureTrace()
    try {
      const [projectResponse, capabilityResponse] = await Promise.all([
        galaxyApi.getProject(projectId, trace),
        galaxyApi.getCapabilities(),
      ])
      const loadedProject = projectResponse.project ?? null
      setProject(loadedProject)
      setCapabilities(capabilityResponse.capabilities ?? null)

      // 看哪个槽：打开这一页总是从第一个启用的槽开始；切槽走 handleSwitchSlot，
      // 它不重新拉工程、也不起加载骨架。
      const loadedSlot = pickSlot(loadedProject, ContentSlot.UNSPECIFIED)
      setSlot(loadedSlot)

      const [draftResponse, versionResponse] = await Promise.all([
        galaxyApi.getDraft(projectId, loadedSlot),
        galaxyApi.listVersions(projectId, loadedSlot),
      ])
      const loadedEntries = draftResponse.draft?.entries ?? []
      setEntries(loadedEntries)
      setDraftUpdatedAt(draftResponse.draft?.updatedAt ?? '')
      setVersions(versionResponse.versions)
      // 默认落在入口文件与它的同目录首项上：那是"打开就看到内容"的位置。
      setPreviewPath(defaultPreviewPath(loadedSlot, loadedEntries))
      setSelectedPath(loadedEntries[0]?.path ?? '')

      // 资产区只在"能力启用且持有读权限"时才请求：未配置私有桶时服务端拿不到
      // 地址，没权限时请求本身就会被拒——两者都不该让整页失败。
      if (capabilityResponse.capabilities?.assetUploadEnabled === true && canReadAssets) {
        // 整页加载从"不筛"开始：用户重开这一页时，看到的该是全部素材。
        setAssetFilter([])
        const assetResponse = await galaxyApi.listAssets(projectId)
        setAssets(assetResponse.assets)
        setAssetTags(assetResponse.projectTags)
      } else {
        setAssets([])
        setAssetTags([])
      }

      // 打开页面就把"这份草稿能不能发布"问出来：用户到这一页本来就是来问这件事的。
      // **桶是内容的前提**：没配置桶时字节没有地方放，也就不存在草稿与版本——
      // 那时不去问校验与预览，改由下面渲染一句说明（见 spec 的"未配置时降级正确"）。
      if (capabilityResponse.capabilities?.assetUploadEnabled === true) {
        await validate(loadedSlot)
      }
      // 预览另有一条前提：它落在发布域上，因此没有发布域时不去取地址（那条路整体
      // 缺席，见 spec 的"没有发布域的部署没有预览"）。
      if (capabilityResponse.capabilities?.previewEnabled === true) {
        await renderPreview(loadedSlot, defaultPreviewPath(loadedSlot, loadedEntries))
      }
      trackEditorOpen(Result.OK, trace)
    } catch (err) {
      trackEditorOpen(Result.FAIL, trace, err)
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setLoading(false)
    }
  }, [projectId, canReadAssets, validate, renderPreview])

  useEffect(() => {
    void load()
  }, [load])

  /**
   * 这一页正在看的草稿变了：重拉草稿、版本与工程，并重新校验、重新渲染预览。
   *
   * 它是订阅（`useWatch`）的落点，也是「刷新」之外唯一的重读入口——命令行 push
   * 之后，状态条不能继续显示上一次的「可以发布」。
   *
   * 不走整页 `load`——那会把加载骨架拉起来，也会把正在看的那一页重置回入口。
   * 拉取失败时把结论标成未完成，而不是留着过期的「可以发布」。
   */
  const refreshOpenDraft = useCallback(async (): Promise<void> => {
    if (projectId === undefined) {
      return
    }
    const seq = ++refreshSeq.current
    const contentEnabled = capabilities?.assetUploadEnabled === true
    try {
      // 拒绝要就地接住：中途因序号作废而返回时，这个请求不能变成未处理的拒绝。
      const assetsPromise =
        contentEnabled && canReadAssets
          ? galaxyApi.listAssets(projectId).then(
              (value) => ({ ok: true as const, value }),
              (err: unknown) => ({ ok: false as const, err }),
            )
          : null
      const [projectResponse, draftResponse, versionResponse] = await Promise.all([
        galaxyApi.getProject(projectId),
        galaxyApi.getDraft(projectId, activeSlot),
        galaxyApi.listVersions(projectId, activeSlot),
      ])
      if (seq !== refreshSeq.current) {
        return
      }
      const loadedProject = projectResponse.project
      if (loadedProject === undefined) {
        setValidation({ status: 'failed', problems: [] })
        setFailure({ message: '工程不存在或已被删除', traceId: null })
        return
      }
      // 槽只增不删，因此重拉之后 activeSlot 仍然有效；但工程换过（或这个槽被
      // 别处加进来之前的那一帧）时退回第一个启用的槽，免得读一个不存在的槽。
      const nextSlot = pickSlot(loadedProject, activeSlot)
      const loadedEntries = draftResponse.draft?.entries ?? []
      const nextPreview = loadedEntries.some((entry) => entry.path === previewPath)
        ? previewPath
        : defaultPreviewPath(nextSlot, loadedEntries)
      const nextSelected = loadedEntries.some((entry) => entry.path === selectedPath)
        ? selectedPath
        : (loadedEntries[0]?.path ?? '')
      setProject(loadedProject)
      setSlot(nextSlot)
      setEntries(loadedEntries)
      setDraftUpdatedAt(draftResponse.draft?.updatedAt ?? '')
      setVersions(versionResponse.versions)
      setPreviewPath(nextPreview)
      setSelectedPath(nextSelected)
      setFailure(null)

      // 预览走发布域上的一条通道：没有发布域时它整条缺席，不去取一条注定失败的
      // 地址（见 docs/design/galaxy/site-model.md 的"预览"）。
      if (previewEnabled) {
        await renderPreview(nextSlot, nextPreview)
        if (seq !== refreshSeq.current) {
          return
        }
      }

      if (contentEnabled) {
        await validate(nextSlot)
        if (seq !== refreshSeq.current) {
          return
        }
        const selectedEntry = loadedEntries.find((entry) => entry.path === nextSelected)
        const shouldReread =
          selectedEntry !== undefined &&
          (mode === 'source' || (sourceText !== '' && selectedEntry.source.case === 'digest'))
        if (shouldReread) {
          await readEntryText(selectedEntry, selectedEntry.path === selectedPath)
        }
      }

      if (assetsPromise !== null) {
        const assetResult = await assetsPromise
        if (seq !== refreshSeq.current) {
          return
        }
        if (assetResult.ok) {
          setAssets(assetResult.value.assets)
        } else {
          setFailure({ message: messageOf(assetResult.err), traceId: traceIdOf(assetResult.err) })
        }
      }
    } catch (err) {
      if (seq !== refreshSeq.current) {
        return
      }
      setValidation({ status: 'failed', problems: [] })
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    }
  }, [
    projectId,
    capabilities,
    canReadAssets,
    activeSlot,
    previewPath,
    selectedPath,
    mode,
    sourceText,
    validate,
    renderPreview,
    readEntryText,
  ])

  // **订阅是唯一一条"回到最新"的路径**，没有"回到前台再读一次"那条兜底：隐藏期间
  // 不退订，事件照常交付；连接真的死了（浏览器冻结标签页只是其中一种成因）由通道
  // 按心跳判活、重连，而重连本身就会带来重拉（见 docs/design/events/README.md 的
  // "页面隐藏时"）。
  //
  // 兜底看着像多一层保险，其实是拿"用户有没有又看向这一页"去猜"连接还活着没有"
  // ——两者相关性很弱，页内换一次焦点就会误触发一次全页重读，表现是"在预览里点过
  // 一下之后，点外壳上任何一个按钮预览都整个重载"。
  //
  // 主题集合就地写出来即可：`useWatch` 按**内容**而不是数组身份判断集合有没有变，
  // 因此每次渲染新建一个数组不会让它重开一条流。加载完成之前集合为空——那时还
  // 没有可订的工程。事件不带"变了什么"，到达即重拉这一页的整组读取（它本来就
  // 是廉价的）。
  useWatch(project === null || projectId === undefined ? [] : [projectTopic(projectId)], () => {
    void refreshOpenDraft()
  })

  /** 重新渲染预览，即刷新内容里的短时资产地址。 */
  async function handleRefreshPreview(): Promise<void> {
    setPreviewBusy(true)
    setFailure(null)
    try {
      await loadAssets(assetFilter)
      await renderPreview(activeSlot, previewPath)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setPreviewBusy(false)
    }
  }

  /**
   * 换一个内容槽看。
   *
   * **不重新拉工程，也不起加载骨架**：换的只是"这一页在看哪一块内容"，工程本身
   * 没变。因此先把上一槽的内容清干净（否则会有一帧显示着另一个槽的草稿），再按
   * 新槽把草稿、版本、预览与校验拉一遍。
   */
  async function handleSwitchSlot(next: ContentSlot): Promise<void> {
    if (projectId === undefined || next === activeSlot) {
      return
    }
    setSlot(next)
    setEntries([])
    setVersions([])
    setDraftUpdatedAt('')
    setSelectedPath('')
    setSourceText('')
    setPreviewUrl('')
    setPreviewError(null)
    setValidation(VALIDATION_PENDING)
    setFailure(null)
    try {
      const [draftResponse, versionResponse] = await Promise.all([
        galaxyApi.getDraft(projectId, next),
        galaxyApi.listVersions(projectId, next),
      ])
      const loadedEntries = draftResponse.draft?.entries ?? []
      const nextPreview = defaultPreviewPath(next, loadedEntries)
      setEntries(loadedEntries)
      setDraftUpdatedAt(draftResponse.draft?.updatedAt ?? '')
      setVersions(versionResponse.versions)
      setPreviewPath(nextPreview)
      setSelectedPath(loadedEntries[0]?.path ?? '')
      if (capabilities?.assetUploadEnabled === true) {
        await validate(next)
      }
      if (capabilities?.previewEnabled === true) {
        await renderPreview(next, nextPreview)
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    }
  }

  /**
   * 给这个工程加一个内容槽。
   *
   * **单向操作**：槽只增不删（见 docs/design/galaxy/site-model.md）。加完直接把
   * 这一页切到新槽上——用户点这个动作就是想在那里放内容。**另一个槽的一切不动**，
   * 因此这里不重拉它。
   */
  async function handleAddSlot(next: ContentSlot): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setContentBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.addProjectSlot(projectId, next)
      if (response.project !== undefined) {
        setProject(response.project)
      }
      await handleSwitchSlot(next)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setContentBusy(false)
    }
  }

  /** 源码视图里点一份文件。 */
  async function handleSelectEntry(entry: FileEntry): Promise<void> {
    await readEntryText(entry)
  }

  async function handleSaveVersion(): Promise<void> {
    if (projectId === undefined) {
      // 前端拦下、请求根本没发出去——这正是客户端事件要捕获的形状。
      trackBlocked(Surface.WEB_EDITOR, Action.DRAFT_SAVE)
      return
    }
    setContentBusy(true)
    setFailure(null)
    try {
      // 保存版本冻结的是**服务端该槽的草稿清单**。网页端不改内容，因此这里不需要
      // 先保存草稿——草稿正是命令行刚 push 上来的那一份。
      await galaxyApi.saveVersion(projectId, activeSlot)
      await reloadVersions(activeSlot)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setContentBusy(false)
    }
  }

  async function handlePublish(versionId: string): Promise<void> {
    if (projectId === undefined) {
      trackBlocked(Surface.WEB_EDITOR, Action.PUBLISH)
      return
    }
    setPublishBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.publish(projectId, activeSlot, versionId)
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
      trackBlocked(Surface.WEB_EDITOR, Action.UNPUBLISH)
      return
    }
    setPublishBusy(true)
    setFailure(null)
    try {
      const response = await galaxyApi.unpublish(projectId, activeSlot)
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

  const isDocs = activeSlot === ContentSlot.DOCS
  // **桶是内容的前提**：没配置对象存储时，内容（草稿与版本）整体不可用，
  // 只有工程元数据可写。此时不渲染内容相关的入口与结论，改给一句说明。
  const contentEnabled = capabilities?.assetUploadEnabled === true
  const textEntries = entries.filter((entry) => entry.source.case === 'digest')

  const topBar = (
    <WorkbenchTopBar
      project={project}
      narrow={narrow}
      versions={versions}
      slot={activeSlotState}
      slots={project.slots}
      slotBusy={contentBusy}
      onSwitchSlot={(next) => void handleSwitchSlot(next)}
      onAddSlot={(next) => void handleAddSlot(next)}
      canWrite={canWrite}
      canPublish={canPublish}
      publishEnabled={capabilities?.publishEnabled === true}
      contentEnabled={contentEnabled}
      assetPanelEnabled={capabilities?.assetUploadEnabled === true && canReadAssets}
      versionBusy={contentBusy}
      publishBusy={publishBusy}
      draftHasProblems={validation.status === 'problems'}
      draftEntries={entries}
      onOpenAssets={() => {
        setPanel('assets')
        trackPanelOpen(Action.ASSETS_OPEN)
      }}
      onOpenVersions={() => {
        setPanel('versions')
        trackPanelOpen(Action.VERSIONS_OPEN)
      }}
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
      description="内容的字节没有地方放，因此草稿、版本、预览与发布都不可用。工程信息与内容槽照常可读写。"
    />
  )

  const strip = contentEnabled && (
    <LifecycleStrip
      validation={validation}
      versions={versions}
      slot={activeSlotState}
      publishEnabled={capabilities?.publishEnabled === true}
      canPublish={canPublish}
      publishBusy={publishBusy}
      onRetryValidate={() => void validate(activeSlot)}
      onUnpublish={() => void handleUnpublish()}
      narrow={narrow}
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
      <AppModal
        title="资产"
        open={panel === 'assets'}
        onCancel={() => setPanel(null)}
        footer={null}
        width={860}
        destroyOnHidden
      >
        <AssetLibrary
          projectId={project.id}
          capabilities={{ assetLimits: capabilities?.assetLimits ?? [] }}
          assets={assets}
          projectTags={assetTags}
          filterTags={assetFilter}
          onFilterChange={handleAssetFilterChange}
          canWrite={canWriteAssets}
          onChanged={() => loadAssets(assetFilter)}
        />
      </AppModal>

      <AppModal
        title="版本"
        open={panel === 'versions'}
        onCancel={() => setPanel(null)}
        footer={null}
        width={640}
        destroyOnHidden
      >
        <VersionList
          projectId={project.id}
          slot={activeSlot}
          versions={versions}
          canWrite={canWrite}
          onChanged={() => reloadVersions(activeSlot)}
        />
      </AppModal>
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
  // 预览走发布域上的一条通道，因此**没有发布域的部署就没有预览**：此时不渲染预览
  // 这个模式，只留只读的源码视图（如实缺席，见 docs/design/galaxy/site-model.md）。
  const previewEnabled = capabilities?.previewEnabled === true
  const stageMode: StageMode = previewEnabled ? mode : 'source'

  const stage = contentEnabled && (
    <Flex
      vertical
      gap={8}
      style={{ flex: 1, minWidth: 0, minHeight: narrow ? NARROW_STAGE_MIN_HEIGHT : STAGE_MIN_HEIGHT }}
    >
      <Flex align="center" justify="space-between" gap={12} wrap>
        {previewEnabled ? (
          <Segmented<StageMode>
            value={stageMode}
            onChange={(next) => {
              setMode(next)
              track({ surface: Surface.WEB_PREVIEW, action: Action.PREVIEW_TOGGLE, result: Result.OK })
            }}
            options={[
              { value: 'preview', label: '预览', icon: <Eye size={14} /> },
              { value: 'source', label: '源码', icon: <FileCode size={14} /> },
            ]}
          />
        ) : (
          <Typography.Text type="secondary">
            这个部署没有发布域，预览不可用；源码照常可看。
          </Typography.Text>
        )}
        {stageMode === 'preview' ? (
          <Space wrap>
            {!isDocs && textEntries.length > 0 && (
              <Select
                size="small"
                value={previewPath}
                style={{ minWidth: narrow ? 120 : 180 }}
                onChange={(value: string) => {
                  setPreviewPath(value)
                  void renderPreview(activeSlot, value)
                }}
                options={textEntries.map((entry) => ({ value: entry.path, label: entry.path }))}
              />
            )}
            <Button
              href={`/galaxy/${project.id}/preview?slot=${activeSlot === ContentSlot.DOCS ? 'docs' : 'site'}`}
              target="_blank"
              rel="noopener"
              icon={<ExternalLink size={16} />}
              // 窄屏只留图标，名字靠 aria-label 保住（与顶栏/状态条同一条做法）。
              aria-label={narrow ? '单独打开' : undefined}
            >
              {narrow ? null : '单独打开'}
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
      <div style={{ flex: 1, minHeight: 0 }}>
        {stageMode === 'preview' ? (
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
          ) : previewUrl === '' ? (
            // 空地址表示草稿里还没有可预览的入口：这是"还没内容"，不是失败。
            <Empty
              description="草稿还是空的。用命令行 push 一组文件上来。"
              image={Empty.PRESENTED_IMAGE_SIMPLE}
            />
          ) : (
            <SandboxFrame url={previewUrl} title="预览" height="100%" />
          )
        ) : (
          <SourceView
            entries={entries}
            assets={assets}
            selectedPath={selectedPath}
            sourceText={sourceText}
            sourceBusy={sourceBusy}
            onSelect={(entry) => void handleSelectEntry(entry)}
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

  // 一列到底，宽窄都一样：容器占满内容区，那块面积吃掉剩下的高度（它的下限按
  // 宽窄不同，见上面的常量）。**这里不分叉结构**——窄屏的差异全在下面两个 chrome
  // 组件内部（顶栏收成一行 +「更多」、状态条压一行，见 docs/design/web/responsive.md
  // 的「工作台窄屏」），本页只把 narrow 传下去，并收紧一档行距。
  return (
    <Flex vertical gap={narrow ? 8 : 12} style={{ height: '100%', minHeight: 0 }}>
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
 * 默认预览哪一页：`site` 槽取入口页（`index.html`），没有就取第一份文本。
 * `docs` 槽的入口是渲染出来的那一页，由服务端决定，因此这里不给路径。
 */
function defaultPreviewPath(slot: ContentSlot, entries: readonly FileEntry[]): string {
  if (slot === ContentSlot.DOCS) {
    return ''
  }
  const entryPath = slot === ContentSlot.SITE ? 'index.html' : ''
  if (entryPath !== '' && entries.some((entry) => entry.path === entryPath)) {
    return entryPath
  }
  return entries.find((entry) => entry.source.case === 'digest')?.path ?? ''
}

/**
 * 上报一次"打开工程编辑页"。
 *
 * `trace` 是这次加载第一次调用（getProject）的链路标识捕获——这一次打开伴随
 * 多次 RPC，规则见 load 里的注释与 ../../api/call-trace。
 */
function trackEditorOpen(result: Result, trace: TraceCapture, error?: unknown): void {
  track({
    surface: Surface.WEB_EDITOR,
    action: Action.EDITOR_OPEN,
    result,
    traceId: traceIdForAction(trace, error),
  })
}

/**
 * 上报一次"前端拦下、请求没发出去"的动作。
 *
 * 只这一档上报：发布、存版本这类动作**成功与失败都由服务端请求留痕覆盖**（带上
 * client 之后可按端归因），客户端再报一遍只会把同一件事数两遍。
 */
function trackBlocked(surface: Surface, action: Action): void {
  track({ surface, action, result: Result.BLOCKED })
}

/** 上报一次"打开弹层"（资产库 / 版本）。 */
function trackPanelOpen(action: Action): void {
  track({ surface: Surface.WEB_EDITOR, action, result: Result.OK })
}

/**
 * 定出这一页看哪个槽：优先用用户选的那个（工程换了或还没选时退回第一个启用的
 * 槽）。一个工程至少有一个槽，因此只要工程存在就定得下来；工程为空（还没加载
 * 完）时返回零值，调用方那时也不该拿它去发请求。
 */
function pickSlot(project: Project | null, preferred: ContentSlot): ContentSlot {
  if (project === null || project.slots.length === 0) {
    return ContentSlot.UNSPECIFIED
  }
  if (project.slots.some((candidate) => candidate.slot === preferred)) {
    return preferred
  }
  return project.slots[0]?.slot ?? ContentSlot.UNSPECIFIED
}
