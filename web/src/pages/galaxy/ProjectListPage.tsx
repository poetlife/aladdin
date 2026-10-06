import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Empty,
  Form,
  Input,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { TableProps } from 'antd'
import { Plus, RefreshCw, Sparkles, Trash2 } from 'lucide-react'
import { useNavigate } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import { PermissionGate } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import { ContentSlot, type Project, type ProjectSlot } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { AppModal } from '../../ui/AppModal'
import { Action, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'
import { track } from '../../telemetry/track'
import { allSlots, slotDescription, slotLabel } from './content-slot'
import { formatTime } from './format-time'

interface ProjectFormValues {
  name?: string
  description?: string
  slots: ContentSlot[]
}

interface failure {
  message: string
  traceId: string | null
}

/**
 * 我的工程列表。
 *
 * 只有**一个**列表入口，且范围由凭证决定（见 docs/design/galaxy/README.md）：
 * 没有"列出所有工程"的形状。新建与删除是写操作，按 `galaxy.project.write`
 * 裁剪——无权限的入口不渲染，而不是渲染一个点了报错的控件。
 *
 * **列表按槽说话，不问"这个工程是什么形态"**：一个工程可以既是站点又有文档，
 * 两槽各发各的。因此这里有一列内容槽，状态也按槽逐条给出（最多两条）。
 */
export function ProjectListPage(): React.ReactNode {
  const navigate = useNavigate()
  const [form] = Form.useForm<ProjectFormValues>()

  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<failure | null>(null)

  const [createOpen, setCreateOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  // 删工程会连带清版本与资产，请求往往不短。确认框上的 loading 与按钮禁用
  // 都看这一个标识；ref 挡住同一次点击里的第二次提交（state 还没提交上去）。
  const deletingRef = useRef<string | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const [confirmingId, setConfirmingId] = useState<string | null>(null)

  // 撤回发布：**按槽**，因此键是「工程:槽」而不是工程。同一个工程的两个槽各有
  // 各的确认框与提交中状态——撤回一个槽不动另一个（见 publication.md）。
  const unpublishingRef = useRef<string | null>(null)
  const [unpublishingKey, setUnpublishingKey] = useState<string | null>(null)
  const [confirmingUnpublish, setConfirmingUnpublish] = useState<string | null>(null)

  // trackOpen 只在"进入这个页面"时为真：load 在删除之后也会被调用来刷新，
  // 那一次不是"打开列表"，不该再报一条 PROJECT_LIST_OPEN。
  const load = useCallback(async (trackOpen = false): Promise<void> => {
    setLoading(true)
    setFailure(null)
    try {
      const response = await galaxyApi.listProjects()
      setProjects(response.projects)
      if (trackOpen) {
        trackListOpen(Result.OK)
      }
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      setProjects([])
      if (trackOpen) {
        trackListOpen(Result.FAIL, err)
      }
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load(true)
  }, [load])

  async function handleCreate(values: ProjectFormValues): Promise<void> {
    setCreating(true)
    setCreateError(null)
    try {
      const response = await galaxyApi.createProject(
        values.name ?? '',
        values.description ?? '',
        values.slots,
      )
      setCreateOpen(false)
      form.resetFields()
      if (response.project !== undefined) {
        // 新建之后直接进工作台：刚建好的工程是空的，下一步是去把内容推上来。
        void navigate(`/galaxy/${response.project.id}`)
      } else {
        await load()
      }
    } catch (err) {
      setCreateError(messageOf(err))
    } finally {
      setCreating(false)
    }
  }

  async function handleDelete(projectId: string): Promise<void> {
    if (deletingRef.current !== null) {
      return
    }
    deletingRef.current = projectId
    setDeletingId(projectId)
    setConfirmingId(projectId)
    try {
      await galaxyApi.deleteProject(projectId)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      return
    } finally {
      deletingRef.current = null
      setDeletingId(null)
      setConfirmingId(null)
    }
    await load()
  }

  /**
   * 撤回**某一个槽**的发布。
   *
   * 与工作台里那一处是同一个服务端动作：只置空该槽的指针，另一个槽的地址、版本与
   * 状态都不动。列表侧提供它是因为**能看到地址的地方就该能在那里把它作废**——否则
   * 用户得先打开工作台、再去找状态条，而那正是"只能发、不能撤"这个印象的来源。
   */
  async function handleUnpublish(projectId: string, slot: ContentSlot): Promise<void> {
    if (unpublishingRef.current !== null) {
      return
    }
    const key = unpublishKey(projectId, slot)
    unpublishingRef.current = key
    setUnpublishingKey(key)
    setConfirmingUnpublish(key)
    try {
      await galaxyApi.unpublish(projectId, slot)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      return
    } finally {
      unpublishingRef.current = null
      setUnpublishingKey(null)
      setConfirmingUnpublish(null)
    }
    await load()
  }

  const columns: NonNullable<TableProps<Project>['columns']> = [
    {
      title: '名称',
      dataIndex: 'name',
      key: 'name',
      render: (name: string, project) => (
        <Space orientation="vertical" size={0}>
          <Typography.Text strong>{name === '' ? '(未命名)' : name}</Typography.Text>
          <Typography.Text type="secondary" style={{ wordBreak: 'break-all' }}>
            {project.description}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: '更新时间',
      dataIndex: 'updatedAt',
      key: 'updatedAt',
      render: (value: string) => formatTime(value),
    },
    {
      title: '内容槽',
      key: 'slots',
      // 槽只增不删，因此这里只是展示；加槽在工作台的槽切换器那里。
      render: (_: unknown, project) =>
        project.slots.length === 0 ? (
          <Typography.Text type="secondary">无</Typography.Text>
        ) : (
          <Space size={4} wrap>
            {project.slots.map((slot) => (
              <Tag key={slot.slot}>{slotLabel(slot.slot)}</Tag>
            ))}
          </Space>
        ),
    },
    {
      title: '状态',
      key: 'published',
      render: (_: unknown, project) =>
        project.slots.length === 0 ? (
          <Tag>未发布</Tag>
        ) : (
          <Space orientation="vertical" size={4}>
            {project.slots.map((slot) => {
              const key = unpublishKey(project.id, slot.slot)
              return (
                <SlotStatus
                  key={slot.slot}
                  slot={slot}
                  confirming={confirmingUnpublish === key}
                  busy={unpublishingKey === key}
                  anyBusy={unpublishingKey !== null}
                  onConfirmChange={(nextOpen) => {
                    if (unpublishingRef.current !== null) {
                      return
                    }
                    // 取消确认框：动作根本没发生（请求没发出去），服务端留痕里没有它。
                    // 成功与失败则由撤回请求本身留痕覆盖，这里不重复报。
                    if (!nextOpen && confirmingUnpublish === key) {
                      trackUnpublishCancel()
                    }
                    setConfirmingUnpublish(nextOpen ? key : null)
                  }}
                  onUnpublish={() => void handleUnpublish(project.id, slot.slot)}
                />
              )
            })}
          </Space>
        ),
    },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, project) => (
        <Space>
          <Button type="link" onClick={() => void navigate(`/galaxy/${project.id}`)}>
            打开
          </Button>
          <PermissionGate require={PermissionCodes.GalaxyProjectWrite}>
            <Popconfirm
              title="删除这个工程？"
              description="会连带删掉它的全部版本与资产，已发布的地址立刻失效。此操作不可撤销。"
              okText="删除"
              okButtonProps={{ danger: true, loading: deletingId === project.id }}
              open={confirmingId === project.id}
              onOpenChange={(nextOpen) => {
                if (deletingRef.current !== null) {
                  return
                }
                // 取消确认框：动作根本没发生（请求没发出去），服务端留痕里没有它。
                // 成功与失败则由删除请求本身留痕覆盖，这里不重复报。
                if (!nextOpen && confirmingId === project.id) {
                  trackProjectDeleteCancel()
                }
                setConfirmingId(nextOpen ? project.id : null)
              }}
              onConfirm={() => handleDelete(project.id)}
            >
              <Button type="link" danger disabled={deletingId !== null} icon={<Trash2 size={14} />}>
                删除
              </Button>
            </Popconfirm>
          </PermissionGate>
        </Space>
      ),
    },
  ]

  return (
    <Card
      title={
        <Space size={8}>
          <Sparkles size={16} />
          我的工程
        </Space>
      }
      extra={
        <Space>
          <PermissionGate require={PermissionCodes.GalaxyProjectWrite}>
            <Button type="primary" icon={<Plus size={16} />} onClick={() => setCreateOpen(true)}>
              新建工程
            </Button>
          </PermissionGate>
          <Button icon={<RefreshCw size={16} />} onClick={() => void load()}>
            刷新
          </Button>
        </Space>
      }
    >
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
          style={{ marginBottom: 16 }}
        />
      )}

      <Table<Project>
        rowKey="id"
        columns={columns}
        dataSource={projects}
        loading={loading}
        pagination={false}
        // 列宽按内容撑开，手机上让表格在自己的容器里横向滚动，而不是把整页顶宽。
        scroll={{ x: 'max-content' }}
        locale={{
          emptyText: (
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description="还没有工程，新建一个开始创作"
            />
          ),
        }}
      />

      <AppModal
        title="新建工程"
        open={createOpen}
        confirmLoading={creating}
        okText="创建"
        onOk={() => form.submit()}
        onCancel={() => {
          setCreateOpen(false)
          setCreateError(null)
          form.resetFields()
        }}
      >
        {createError !== null && (
          <Alert type="error" title={createError} style={{ marginBottom: 16 }} />
        )}
        <Form<ProjectFormValues>
          form={form}
          layout="vertical"
          initialValues={{ slots: [ContentSlot.SITE] }}
          onFinish={(v) => void handleCreate(v)}
        >
          <Form.Item
            name="slots"
            label="内容槽"
            extra="至少选一个。选两个就是「既有站点又有文档」：两槽各有自己的草稿、版本与发布地址，互不影响。此后可以再加，不能删"
            rules={[{ required: true, message: '至少选一个内容槽' }]}
          >
            <Checkbox.Group
              options={allSlots().map((slot) => ({
                label: `${slotLabel(slot)} · ${slotDescription(slot)}`,
                value: slot,
              }))}
            />
          </Form.Item>
          <Form.Item name="name" label="名称" extra="仅用于你自己识别，不是地址、不需要唯一">
            <Input maxLength={64} placeholder="比如：我的首页" autoComplete="off" />
          </Form.Item>
          <Form.Item name="description" label="简介" extra="可留空">
            <Input.TextArea maxLength={280} rows={3} placeholder="随便写点什么" />
          </Form.Item>
        </Form>
      </AppModal>
    </Card>
  )
}

/** 上报一次"打开工程列表"。失败时带上 trace_id 以便与服务端留痕关联。 */
function trackListOpen(result: Result, error?: unknown): void {
  track({
    surface: Surface.WEB_PROJECT_LIST,
    action: Action.PROJECT_LIST_OPEN,
    result,
    traceId: error === undefined ? undefined : (traceIdOf(error) ?? undefined),
  })
}

/** 上报一次"取消删除工程"：动作根本没发生，请求留痕里没有它。 */
function trackProjectDeleteCancel(): void {
  track({
    surface: Surface.WEB_PROJECT_LIST,
    action: Action.PROJECT_DELETE,
    result: Result.CANCEL,
  })
}

/** 上报一次"取消撤回发布"：同上，动作根本没发生。 */
function trackUnpublishCancel(): void {
  track({
    surface: Surface.WEB_PROJECT_LIST,
    action: Action.UNPUBLISH,
    result: Result.CANCEL,
  })
}

/** 一个（工程，槽）在撤回那件事上的键。**按槽**：两个槽各有各的确认与提交中状态。 */
function unpublishKey(projectId: string, slot: ContentSlot): string {
  return `${projectId}:${slot}`
}

interface SlotStatusProps {
  slot: ProjectSlot
  /** 这一个槽的确认框是否开着。 */
  confirming: boolean
  /** 这一个槽正在撤回。 */
  busy: boolean
  /** 别的槽正在撤回（或删除）时不让再开一个确认框。 */
  anyBusy: boolean
  onConfirmChange: (open: boolean) => void
  onUnpublish: () => void
}

/**
 * 一个内容槽的发布状态：已发布时把该槽的地址一并给出来，**并在地址旁给出撤回**。
 *
 * 撤回放在这里而不是"操作"列，与工作台是同一条原则：它是那一处状态的逆操作，
 * 跟着它要作废的那个地址走（见 docs/design/galaxy/authoring.md）。能看到地址的
 * 地方就该能在那里把它作废——否则用户得先打开工作台、再去找状态条。
 *
 * 按 `galaxy.project.publish` 裁剪：不持有发布权限时只显示地址，不渲染撤回。
 */
function SlotStatus({
  slot,
  confirming,
  busy,
  anyBusy,
  onConfirmChange,
  onUnpublish,
}: SlotStatusProps): React.ReactNode {
  if (!slot.published) {
    return (
      <Space size={4}>
        <Tag>{slotLabel(slot.slot)}</Tag>
        <Typography.Text type="secondary">未发布</Typography.Text>
      </Space>
    )
  }
  return (
    <Space orientation="vertical" size={0}>
      <Space size={4}>
        <Tag color="success">已发布</Tag>
        <Tag>{slotLabel(slot.slot)}</Tag>
      </Space>
      <Space size={4} wrap>
        <Typography.Text type="secondary" copyable style={{ wordBreak: 'break-all' }}>
          {slot.publishedUrl}
        </Typography.Text>
        <PermissionGate require={PermissionCodes.GalaxyProjectPublish}>
          <Popconfirm
            title="撤回这个槽的发布？"
            description="这条地址会立刻不可达。发布记录保留，之后可以重新发布同一个版本；另一个槽不受影响。"
            okText="撤回"
            okButtonProps={{ danger: true, loading: busy }}
            open={confirming}
            onOpenChange={onConfirmChange}
            onConfirm={onUnpublish}
          >
            <Button type="link" size="small" danger disabled={anyBusy} loading={busy}>
              撤回发布
            </Button>
          </Popconfirm>
        </PermissionGate>
      </Space>
    </Space>
  )
}
