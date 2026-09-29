import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Empty,
  Form,
  Input,
  Modal,
  Popconfirm,
  Radio,
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
import { SiteForm, type Project } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { formatTime } from './format-time'

interface ProjectFormValues {
  name?: string
  description?: string
  form: SiteForm
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

  const load = useCallback(async (): Promise<void> => {
    setLoading(true)
    setFailure(null)
    try {
      const response = await galaxyApi.listProjects()
      setProjects(response.projects)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      setProjects([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function handleCreate(values: ProjectFormValues): Promise<void> {
    setCreating(true)
    setCreateError(null)
    try {
      const response = await galaxyApi.createProject(
        values.name ?? '',
        values.description ?? '',
        values.form,
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
      title: '形态',
      key: 'form',
      // 形态创建时定下、此后不可改，因此这里只是展示，没有切换入口。
      render: (_: unknown, project) => (
        <Tag>{project.form === SiteForm.DOCS ? 'docs' : 'static'}</Tag>
      ),
    },
    {
      title: '状态',
      key: 'published',
      render: (_: unknown, project) =>
        project.published ? (
          <Space orientation="vertical" size={0}>
            <Tag color="success">已发布</Tag>
            <Typography.Text type="secondary" copyable style={{ wordBreak: 'break-all' }}>
              {project.publishedUrl}
            </Typography.Text>
          </Space>
        ) : (
          <Tag>未发布</Tag>
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

      <Modal
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
          initialValues={{ form: SiteForm.STATIC }}
          onFinish={(v) => void handleCreate(v)}
        >
          <Form.Item
            name="form"
            label="形态"
            extra="创建时定下，此后不可改：它决定已保存版本的发布语义"
          >
            <Radio.Group>
              <Radio.Button value={SiteForm.STATIC}>static · 整站文件原样服务</Radio.Button>
              <Radio.Button value={SiteForm.DOCS}>docs · markdown 渲染成多页</Radio.Button>
            </Radio.Group>
          </Form.Item>
          <Form.Item name="name" label="名称" extra="仅用于你自己识别，不是地址、不需要唯一">
            <Input maxLength={64} placeholder="比如：我的首页" autoComplete="off" />
          </Form.Item>
          <Form.Item name="description" label="简介" extra="可留空">
            <Input.TextArea maxLength={280} rows={3} placeholder="随便写点什么" />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  )
}
