import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Card, Form, Input, Space, Typography } from 'antd'
import { Eye, FileCode, Images, Layers, ListChecks, Pencil, Rocket, Save } from 'lucide-react'
import { useParams } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import { usePermission } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import type {
  Asset,
  Capabilities,
  Project,
  ValidationProblem,
  Version,
} from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { AssetLibrary } from './AssetLibrary'
import { PreviewFrame } from './PreviewFrame'
import { PublishPanel } from './PublishPanel'
import { VersionList } from './VersionList'
import { formatTime } from './format-time'

interface ProjectFormValues {
  name?: string
  description?: string
}

interface failure {
  message: string
  traceId: string | null
}

const MONOSPACE = 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace'

/**
 * 工程编辑器。
 *
 * 路由已由 RequirePermission 保证 `galaxy.project.read`；页面内部按更细的
 * 权限码裁剪写操作（保存草稿/版本用 write、发布用 publish、资产用 asset.*）。
 *
 * 编辑器**不自己判断正文对不对**：点「校验正文」调服务端的 ValidateContent，
 * 把 problems 逐条列出（见 docs/design/galaxy/authoring.md 的"即时提示走服务端
 * 同一个入口"）。预览与校验是两件事，预览通过不等于发布通过。
 */
export function ProjectEditorPage(): React.ReactNode {
  const { projectId } = useParams<{ projectId: string }>()
  const [form] = Form.useForm<ProjectFormValues>()

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

  const [metaBusy, setMetaBusy] = useState(false)
  const [metaSaved, setMetaSaved] = useState(false)
  const [draftBusy, setDraftBusy] = useState(false)
  const [draftSaved, setDraftSaved] = useState(false)
  const [problems, setProblems] = useState<ValidationProblem[] | null>(null)
  const [validatedOk, setValidatedOk] = useState(false)
  const [loadedSeq, setLoadedSeq] = useState<number | null>(null)

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
      setProject(projectResponse.project ?? null)
      setCapabilities(capabilityResponse.capabilities ?? null)

      const [draftResponse, versionResponse] = await Promise.all([
        galaxyApi.getDraft(projectId),
        galaxyApi.listVersions(projectId),
      ])
      setDraft(draftResponse.draft?.content ?? '')
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
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setLoading(false)
    }
  }, [projectId, canReadAssets])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (project !== null) {
      form.setFieldsValue({ name: project.name, description: project.description })
    }
  }, [project, form])

  async function handleSaveMeta(values: ProjectFormValues): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setMetaBusy(true)
    setMetaSaved(false)
    try {
      const response = await galaxyApi.updateProject(
        projectId,
        values.name ?? '',
        values.description ?? '',
      )
      setProject(response.project ?? null)
      setMetaSaved(true)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setMetaBusy(false)
    }
  }

  async function handleSaveDraft(): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setDraftBusy(true)
    setDraftSaved(false)
    try {
      const response = await galaxyApi.saveDraft(projectId, draft)
      setDraftSavedAt(response.draft?.updatedAt ?? '')
      setDraftSaved(true)
      setLoadedSeq(null)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setDraftBusy(false)
    }
  }

  async function handleSaveVersion(): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setDraftBusy(true)
    setDraftSaved(false)
    try {
      // 保存版本快照的是**服务端的草稿**，所以先把编辑器里的内容落成草稿，
      // 再保存版本——否则版本会停留在上一次保存的草稿上。
      await galaxyApi.saveDraft(projectId, draft)
      await galaxyApi.saveVersion(projectId)
      await reloadVersions()
      setDraftSaved(true)
      setLoadedSeq(null)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setDraftBusy(false)
    }
  }

  async function handleValidate(): Promise<void> {
    if (projectId === undefined) {
      return
    }
    setDraftBusy(true)
    setProblems(null)
    setValidatedOk(false)
    try {
      const response = await galaxyApi.validateContent(projectId, draft)
      setProblems(response.problems)
      setValidatedOk(response.problems.length === 0)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setDraftBusy(false)
    }
  }

  if (loading && project === null) {
    return (
      <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
        <Card title="工程" loading />
        <Card title="正文" loading />
        <Card title="预览" loading />
      </Space>
    )
  }

  if (project === null) {
    return (
      <Card title="工程">
        <Alert
          type="error"
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
      </Card>
    )
  }

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <Pencil size={16} />
            工程
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
        {metaSaved && <Alert type="success" title="已保存" style={{ marginBottom: 16 }} />}
        <Form<ProjectFormValues>
          form={form}
          layout="vertical"
          onFinish={(values) => void handleSaveMeta(values)}
          onValuesChange={() => setMetaSaved(false)}
        >
          <Form.Item name="name" label="名称" extra="仅用于你自己识别，不是地址、不需要唯一">
            <Input maxLength={64} disabled={!canWrite} autoComplete="off" />
          </Form.Item>
          <Form.Item name="description" label="简介" extra="可留空">
            <Input.TextArea maxLength={280} rows={3} disabled={!canWrite} />
          </Form.Item>
          {canWrite && (
            <Button type="primary" htmlType="submit" loading={metaBusy}>
              保存
            </Button>
          )}
        </Form>
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          工程标识：<Typography.Text code>{project.id}</Typography.Text>
        </Typography.Paragraph>
      </Card>

      <Card
        title={
          <Space size={8}>
            <FileCode size={16} />
            正文
          </Space>
        }
        extra={
          <Typography.Text type="secondary">
            {draftSavedAt === '' ? '草稿尚未保存' : `草稿保存于 ${formatTime(draftSavedAt)}`}
          </Typography.Text>
        }
      >
        {loadedSeq !== null && (
          <Alert
            type="info"
            showIcon
            title={`已载入版本 #${loadedSeq} 的正文，尚未保存`}
            style={{ marginBottom: 12 }}
          />
        )}
        {draftSaved && <Alert type="success" title="草稿已保存" style={{ marginBottom: 12 }} />}
        {problems !== null && problems.length > 0 && (
          <Alert
            type="warning"
            showIcon
            title="正文有以下问题，发布会被拒绝"
            description={
              <ul style={{ margin: 0, paddingInlineStart: 20 }}>
                {problems.map((problem, index) => (
                  <li key={index}>{problem.message}</li>
                ))}
              </ul>
            }
            style={{ marginBottom: 12 }}
          />
        )}
        {validatedOk && (
          <Alert type="success" showIcon title="校验通过，这段正文可以发布" style={{ marginBottom: 12 }} />
        )}
        <Input.TextArea
          value={draft}
          onChange={(event) => {
            setDraft(event.target.value)
            setDraftSaved(false)
            setValidatedOk(false)
            setProblems(null)
          }}
          rows={20}
          spellCheck={false}
          placeholder="<!doctype html> 起手，写一份完整的 HTML 文档；素材用 asset://<资产标识> 引用"
          style={{ fontFamily: MONOSPACE, fontSize: 13 }}
        />
        <Space wrap style={{ marginTop: 12 }}>
          {canWrite && (
            <>
              <Button
                icon={<Save size={16} />}
                loading={draftBusy}
                onClick={() => void handleSaveDraft()}
              >
                保存草稿
              </Button>
              <Button loading={draftBusy} onClick={() => void handleSaveVersion()}>
                保存版本
              </Button>
            </>
          )}
          <Button
            icon={<ListChecks size={16} />}
            loading={draftBusy}
            onClick={() => void handleValidate()}
          >
            校验正文
          </Button>
        </Space>
      </Card>

      <Card
        title={
          <Space size={8}>
            <Eye size={16} />
            预览
          </Space>
        }
      >
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          预览是沙箱渲染，正文里的脚本读不到编辑器的任何数据；预览不做裁剪，
          坏引用就显示坏的。预览通过不等于发布通过。
        </Typography.Paragraph>
        <PreviewFrame content={draft} assets={assets} />
      </Card>

      <Card
        title={
          <Space size={8}>
            <Layers size={16} />
            版本
          </Space>
        }
      >
        <VersionList
          projectId={project.id}
          versions={versions}
          canWrite={canWrite}
          onReadBack={(content, seq) => {
            setDraft(content)
            setLoadedSeq(seq)
            setValidatedOk(false)
            setProblems(null)
          }}
          onChanged={reloadVersions}
        />
      </Card>

      {capabilities?.publishEnabled === true && (
        <Card
          title={
            <Space size={8}>
              <Rocket size={16} />
              发布
            </Space>
          }
        >
          <PublishPanel
            projectId={project.id}
            project={project}
            versions={versions}
            canPublish={canPublish}
            onProjectChange={setProject}
          />
        </Card>
      )}

      {capabilities?.assetUploadEnabled === true && canReadAssets && (
        <Card
          title={
            <Space size={8}>
              <Images size={16} />
              资产库
            </Space>
          }
        >
          <AssetLibrary
            projectId={project.id}
            capabilities={{ assetLimits: capabilities.assetLimits }}
            assets={assets}
            canWrite={canWriteAssets}
            onChanged={loadAssets}
          />
        </Card>
      )}
    </Space>
  )
}
