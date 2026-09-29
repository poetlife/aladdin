import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Flex, Skeleton, Space, Typography } from 'antd'
import { ArrowLeft, RefreshCw } from 'lucide-react'
import { useNavigate, useParams } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import { usePermission } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import type { Asset, Project } from '../../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { PreviewFrame } from './PreviewFrame'

interface failure {
  message: string
  traceId: string | null
}

/**
 * 单独打开的预览页。
 *
 * 工作台里预览与源码共用一块面积、切换着看，因此**"改的时候看不见渲染结果"**。
 * 这一页就是那件事的出口：它把同一份草稿铺满整个内容区，用来和别处对照
 * （对着设计稿改、投到另一块屏）。见 docs/design/galaxy/authoring.md 的"编辑页的形态"。
 *
 * 它读的是**草稿**，不是发布产物——与工作台里的预览是同一份内容、同一个渲染方式
 * （复用 `PreviewFrame`，沙箱属性因此不可能与内嵌时漂移）。
 *
 * 资产地址是短时的，长时间挂着会过期；**刷新**按钮重新读取资产清单即得到新地址
 * （与"重新读取档案即得到新头像地址"同源）。
 *
 * 它不需要写权限，只要 `galaxy.project.read`——看一眼草稿不该要求能改它。
 */
export function PreviewPage(): React.ReactNode {
  const { projectId } = useParams<{ projectId: string }>()
  const navigate = useNavigate()

  const canReadAssets = usePermission(PermissionCodes.GalaxyAssetRead)

  const [project, setProject] = useState<Project | null>(null)
  const [content, setContent] = useState('')
  const [assets, setAssets] = useState<Asset[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<failure | null>(null)

  const load = useCallback(async (): Promise<void> => {
    if (projectId === undefined) {
      return
    }
    const [projectResponse, draftResponse] = await Promise.all([
      galaxyApi.getProject(projectId),
      galaxyApi.getDraft(projectId),
    ])
    if (projectResponse.project === undefined) {
      throw new Error('工程不存在或已被删除')
    }
    setProject(projectResponse.project)
    setContent(draftResponse.draft?.content ?? '')
    if (canReadAssets) {
      const assetResponse = await galaxyApi.listAssets(projectId)
      setAssets(assetResponse.assets)
    } else {
      setAssets([])
    }
  }, [projectId, canReadAssets])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setFailure(null)
    void load()
      .catch((err: unknown) => {
        if (!cancelled) {
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
          <Typography.Text type="secondary">草稿预览</Typography.Text>
        </Space>
        <Button
          icon={<RefreshCw size={16} />}
          loading={busy}
          disabled={!canReadAssets}
          onClick={() => void handleRefresh()}
        >
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
        <PreviewFrame content={content} assets={assets} height="100%" />
      </div>
    </Flex>
  )
}
