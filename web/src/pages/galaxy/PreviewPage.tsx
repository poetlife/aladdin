import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Empty, Flex, Skeleton, Space, Typography } from 'antd'
import { ArrowLeft, RefreshCw } from 'lucide-react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { captureTrace, traceIdForAction, type TraceCapture } from '../../api/call-trace'
import { messageOf, traceIdOf } from '../../api/errors'
import { ContentSlot, type Project } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { track } from '../../telemetry/track'
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
  const load = useCallback(async (trace?: TraceCapture): Promise<void> => {
    if (projectId === undefined) {
      return
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
    } catch (err) {
      setUrl('')
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    }
  }, [projectId, requestedSlot])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setFailure(null)
    const trace = captureTrace()
    void load(trace)
      .then(() => {
        if (!cancelled) {
          // 落地成功：这条事件回答"独立预览页被打开并渲染出来了没有"，
          // 而请求留痕只答得了其中每一次 RPC。
          track({
            surface: Surface.WEB_PREVIEW,
            action: Action.PREVIEW_OPEN,
            result: Result.OK,
            traceId: traceIdForAction(trace),
          })
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          track({
            surface: Surface.WEB_PREVIEW,
            action: Action.PREVIEW_OPEN,
            result: Result.FAIL,
            traceId: traceIdForAction(trace, err),
          })
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
  }, [load])

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
          <SandboxFrame url={url} title="预览" height="100%" />
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
