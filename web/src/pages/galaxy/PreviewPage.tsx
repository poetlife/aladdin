import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, Button, Empty, Flex, Skeleton, Space, Typography } from 'antd'
import { ArrowLeft, RefreshCw } from 'lucide-react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { captureTrace, traceIdForAction, type TraceCapture } from '../../api/call-trace'
import { messageOf, traceIdOf } from '../../api/errors'
import { ContentSlot, type Project } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { startTimer, type ActionTimer } from '../../telemetry/track'
import { slotDescription, slotFromName, slotLabel } from './content-slot'
import { SandboxFrame } from './SandboxFrame'

interface failure {
  message: string
  traceId: string | null
}

/**
 * 单独打开的预览页。
 *
 * 工作台里预览与源码共用一块面积、切换着看，因此**"看渲染结果的时候看不了源
 * 文件"**。这一页就是那件事的出口：它把同一份草稿铺满整个内容区，用来和别处对照。
 * 见 docs/design/galaxy/authoring.md 的"这一页的形态"。
 *
 * 它给的是**某一个内容槽草稿整站的地址**（与工作台里的预览是同一条通道、同一个
 * 入口），因此沙箱属性与内容不可能与内嵌时漂移。看哪个槽由地址里的 `?slot=` 给出
 * ——工作台的「单独打开」会带上当前那个槽；没带时落在第一个启用的槽上。地址带短时
 * 凭证，过期即打不开。
 *
 * 地址是短时的，长时间挂着会过期；**刷新**按钮重新取一次即得到新地址。
 *
 * 它不需要写权限，只要 `galaxy.project.read`——看一眼草稿不该要求能改它。
 */
export function PreviewPage(): React.ReactNode {
  const { projectId } = useParams<{ projectId: string }>()
  const [searchParams] = useSearchParams()
  const requestedSlot = searchParams.get('slot')
  const navigate = useNavigate()

  const [project, setProject] = useState<Project | null>(null)
  const [slot, setSlot] = useState<ContentSlot>(ContentSlot.UNSPECIFIED)
  const [url, setUrl] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<failure | null>(null)

  // trace 由调用方给：这一页的两次加载里只有"进入页面"那一次产生 PREVIEW_OPEN
  // 事件（刷新不产生），而事件要带的是这次加载第一次调用（getProject）的链路标识。
  // 放在调用方而不是这里 new 一份，是为了让事件的取值与它描述的那次加载是同一个
  // 对象（见 ../../api/call-trace）。
  //
  // 返回这一次拿到的预览地址（空表示没有可预览的入口）：调用方据此判断"有没有帧
  // 可等"——那决定了这次打开的事件什么时候报、耗时到哪一刻结束。
  const load = useCallback(async (trace?: TraceCapture): Promise<string> => {
    if (projectId === undefined) {
      return ''
    }
    const projectResponse = await galaxyApi.getProject(projectId, trace)
    const loadedProject = projectResponse.project
    if (loadedProject === undefined) {
      throw new Error('工程不存在或已被删除')
    }
    setProject(loadedProject)
    // 地址里的槽要在**这个工程启用的那些**里取：没带或认不出来时退回第一个。
    const target = previewSlot(loadedProject, requestedSlot)
    setSlot(target)
    // 取地址与读工程分开：取地址失败只影响这一块，不把整页打成"工程不存在"。
    try {
      const previewResponse = await galaxyApi.previewDraft(projectId, target)
      setUrl(previewResponse.url)
      setFailure(null)
      return previewResponse.url
    } catch (err) {
      setUrl('')
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      return ''
    }
  }, [projectId, requestedSlot])

  // trace 与计时器都放在 ref 里：这一次打开的结论要等到帧的 `load` 才落地，而那时
  // 上面那个 effect 的闭包早就过去了。两者在 effect 开头一次设定，之后只读。
  const openTrace = useRef<TraceCapture | null>(null)
  const openTimer = useRef<ActionTimer | null>(null)

  /**
   * 这一次"打开预览页"到此为止：报一条事件，带上传到目前为止的耗时。
   *
   * **一次打开只报一条**，因此它把计时器用掉就置空——帧的 `load` 与"没有帧可等"
   * 这两条路都可能走到这里，而它们是同一次打开的两个出口，不是两件事。
   */
  const finishOpen = useCallback((result: Result, error?: unknown): void => {
    const timer = openTimer.current
    const trace = openTrace.current
    // 两者总是同时设定，因此一次判空就够——它挡的是"effect 还没跑就被回调"这种
    // 只可能出现在测试里的时序。
    if (timer === null || trace === null) {
      return
    }
    openTimer.current = null
    timer.end({
      surface: Surface.WEB_PREVIEW,
      action: Action.PREVIEW_OPEN,
      result,
      traceId: traceIdForAction(trace, error),
    })
  }, [])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setFailure(null)
    const trace = captureTrace()
    openTrace.current = trace
    openTimer.current = startTimer()
    void load(trace)
      .then((previewUrl) => {
        if (cancelled) {
          return
        }
        // 有帧可等时**不在这里报**：这条事件回答的是"独立预览页被渲染出来了没有"，
        // 而那一刻是帧的 `load`（见 SandboxFrame 的 onSettled）——请求留痕只答得了
        // 其中每一次 RPC，"内容真的出现了"只有客户端知道。耗时也到那时才结束。
        if (previewUrl !== '') {
          return
        }
        // 空地址表示草稿里还没有可预览的入口：根本没有帧可等，这一次打开到此为止。
        finishOpen(Result.OK)
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          finishOpen(Result.FAIL, err)
          setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })
    return () => {
      cancelled = true
    }
  }, [load, finishOpen])

  async function handleRefresh(): Promise<void> {
    setBusy(true)
    setFailure(null)
    try {
      await load()
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusy(false)
    }
  }

  if (loading && project === null) {
    return <Skeleton active paragraph={{ rows: 6 }} />
  }

  if (project === null) {
    return (
      <Alert
        type="error"
        showIcon
        title={failure?.message ?? '读取工程失败'}
        action={<Button onClick={() => void handleRefresh()}>重试</Button>}
      />
    )
  }

  return (
    <Flex vertical gap={12} style={{ height: '100%', minHeight: 0 }}>
      <Flex align="center" justify="space-between" gap={12} wrap>
        <Space size={4} wrap>
          <Button
            type="text"
            size="small"
            icon={<ArrowLeft size={16} />}
            onClick={() => void navigate(`/galaxy/${project.id}`)}
          >
            回到工作台
          </Button>
          <Typography.Text strong>
            {project.name === '' ? '(未命名)' : project.name}
          </Typography.Text>
          <Typography.Text type="secondary">
            {slotLabel(slot)}草稿预览 · {slotDescription(slot)}
          </Typography.Text>
        </Space>
        <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void handleRefresh()}>
          刷新
        </Button>
      </Flex>

      {failure !== null && (
        <Alert
          type="error"
          showIcon
          title={failure.message}
          description={
            failure.traceId !== null && (
              <Typography.Text type="secondary" copyable>
                追踪 ID：{failure.traceId}
              </Typography.Text>
            )
          }
          action={<Button onClick={() => void handleRefresh()}>重试</Button>}
        />
      )}

      <div style={{ flex: 1, minHeight: 320 }}>
        {url === '' && failure === null ? (
          // 空地址且没有失败，表示草稿里还没有可预览的入口："还没内容"，不是故障。
          <Empty
            description="草稿还是空的。用命令行 push 一组文件上来。"
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            style={{ paddingTop: 48 }}
          />
        ) : (
          // 预览**不做审查、不做裁剪**：内容写什么就渲染什么，坏引用就显示坏的。
          // "这处有问题"由状态条上的校验结论单独给出——把两者混起来会让用户以为
          // 预览看起来对就等于发布能成功（见 docs/design/galaxy/authoring.md）。
          //
          // 帧有了结论才报这次打开：到齐与到点未到齐都算出结论，因此**不会**出现
          // "预览最慢的那一次反而没有事件"（见 SandboxFrame 的 onSettled）。
          <SandboxFrame
            url={url}
            title="预览"
            height="100%"
            onSettled={() => finishOpen(Result.OK)}
          />
        )}
      </div>
    </Flex>
  )
}

/**
 * 定出这一页预览哪个槽：地址里给了、且这个工程确实启用了它，就用它；否则退回
 * 第一个启用的槽。
 *
 * **"启用了它"这件事由工程本身回答**，不由地址回答——地址可以是任何字符串。
 * 槽只增不删，因此一个工程至少有一个槽，退回去的方向总是存在的。
 */
function previewSlot(project: Project, requested: string | null): ContentSlot {
  const wanted = slotFromName(requested)
  if (wanted !== undefined && project.slots.some((candidate) => candidate.slot === wanted)) {
    return wanted
  }
  return project.slots[0]?.slot ?? ContentSlot.UNSPECIFIED
}
