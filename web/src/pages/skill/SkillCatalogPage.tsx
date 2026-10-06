import { useCallback, useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Empty,
  Form,
  Input,
  List,
  Popconfirm,
  Select,
  Space,
  Tag,
  Typography,
} from 'antd'
import { Download, RefreshCw, Star, Trash2 } from 'lucide-react'
import { Link } from 'react-router-dom'

import * as skillApi from '../../api/skill'
import { messageOf, traceIdOf } from '../../api/errors'
import { PermissionGate, usePermission } from '../../auth'
import { PermissionCodes } from '../../gen/permission-codes'
import type { Skill, SkillCapabilities } from '../../gen/proto/aladdin/skill/v1/skill_pb'
import { AppModal } from '../../ui/AppModal'

interface ImportFormValues {
  repositoryUrl: string
  ref?: string
  subPath?: string
  title?: string
  summary?: string
  tags?: string[]
}

/** 一次失败的结论。与 galaxy 列表页同构：消息与链路标识分开带着。 */
interface failure {
  message: string
  traceId: string | null
}

/**
 * 平台技能目录。
 *
 * **它是"平台提供了一组什么"的那一眼**，不是第二个创作界面：选用一个技能的方式
 * 是把它取到自己的工作里（命令行 `aladdin skill get`，或 agent 自己取），这一页
 * 负责让人找到它。目录内容由 **skill.catalog.write** 的主体维护，因此纳管与每一条
 * 上的同步、回滚、删除都按那个权限码裁剪——无权限的入口不渲染，而不是渲染一个
 * 点了报错的控件。
 *
 * **这一页不写任何文件。** 没有"下载"或"安装"这样的动作：平台是真相源，本机不
 * 装副本（见 docs/design/skill/agent-access.md）。
 */
export function SkillCatalogPage(): React.ReactNode {
  const [form] = Form.useForm<ImportFormValues>()
  const canCurate = usePermission(PermissionCodes.SkillCatalogWrite)

  const [skills, setSkills] = useState<Skill[]>([])
  const [availableTags, setAvailableTags] = useState<string[]>([])
  const [truncated, setTruncated] = useState(false)
  const [capabilities, setCapabilities] = useState<SkillCapabilities | null>(null)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<failure | null>(null)

  // 筛选条件。它们直接决定每次请求的入参，因此都放在一处。
  const [query, setQuery] = useState('')
  const [tags, setTags] = useState<string[]>([])
  const [favoritedOnly, setFavoritedOnly] = useState(false)

  const [importOpen, setImportOpen] = useState(false)
  const [importing, setImporting] = useState(false)
  const [importError, setImportError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const resp = await skillApi.listSkills(query, tags, favoritedOnly)
      setSkills(resp.skills)
      setAvailableTags(resp.availableTags)
      setTruncated(resp.truncated)
      setFailure(null)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setLoading(false)
    }
  }, [query, tags, favoritedOnly])

  useEffect(() => {
    void load()
  }, [load])

  // 能力来自服务端，前端不猜：没有配置对象存储时目录整体不可用，这一页据此说明
  // 情况，而不是渲染一排点了报错的按钮（见 docs/design/skill/README.md）。
  useEffect(() => {
    let cancelled = false
    void skillApi
      .getCapabilities()
      .then((resp) => {
        if (!cancelled) {
          setCapabilities(resp.capabilities ?? null)
        }
      })
      .catch(() => {
        // 拿不到能力不下结论：列表本身会给出真实的失败原因。
      })
    return () => {
      cancelled = true
    }
  }, [])

  async function toggleFavorite(item: Skill): Promise<void> {
    setBusyId(item.id)
    try {
      await skillApi.setFavorite(item.id, !item.favorited)
      await load()
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusyId(null)
    }
  }

  async function runAction(id: string, action: () => Promise<unknown>): Promise<void> {
    setBusyId(id)
    try {
      await action()
      await load()
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
    } finally {
      setBusyId(null)
    }
  }

  async function submitImport(): Promise<void> {
    const values = await form.validateFields()
    setImporting(true)
    setImportError(null)
    try {
      const created = await skillApi.importSkill({
        repositoryUrl: values.repositoryUrl,
        ref: values.ref,
        subPath: values.subPath,
        title: values.title,
        summary: values.summary,
        tags: values.tags,
      })
      setImportOpen(false)
      form.resetFields()
      await load()
      // 纳管的结果里带标识：把它摆出来，管理员下一步多半要去详情页看文件清单。
      setFailure(null)
      setQuery(created.title)
    } catch (err) {
      // **纳管失败要把原因留在框里**：这份大小写、这一条路径、远端哪一步没过都
      // 是要照着改的东西，关掉弹窗就丢了。
      setImportError(messageOf(err))
    } finally {
      setImporting(false)
    }
  }

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title="平台技能"
        extra={
          <Space>
            <PermissionGate require={PermissionCodes.SkillCatalogWrite}>
              <Button type="primary" icon={<Download size={16} />} onClick={() => setImportOpen(true)}>
                从 GitHub 纳管
              </Button>
            </PermissionGate>
            <Button icon={<RefreshCw size={16} />} onClick={() => void load()}>
              刷新
            </Button>
          </Space>
        }
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Space wrap>
            <Input.Search
              allowClear
              placeholder="搜标题或简介"
              style={{ width: 260 }}
              onSearch={(value) => setQuery(value)}
              onChange={(event) => {
                if (event.target.value === '') {
                  setQuery('')
                }
              }}
            />
            <Select
              mode="multiple"
              allowClear
              placeholder="按标签筛选（多选取交集）"
              style={{ minWidth: 260 }}
              value={tags}
              options={availableTags.map((tag) => ({ value: tag, label: tag }))}
              onChange={(value: string[]) => setTags(value)}
            />
            <Checkbox
              checked={favoritedOnly}
              onChange={(event) => setFavoritedOnly(event.target.checked)}
            >
              只看我收藏的
            </Checkbox>
          </Space>

          {failure !== null && (
            <Alert
              type="error"
              showIcon
              message={failure.message}
              description={failure.traceId !== null ? `trace_id：${failure.traceId}` : undefined}
            />
          )}

          {capabilities !== null && !capabilities.catalogEnabled && (
            <Alert
              type="info"
              showIcon
              message="这个部署没有配置对象存储，技能目录不可用"
              description="技能的字节没有地方放，因此检索与取用都不可用。"
            />
          )}

          {truncated && (
            <Alert type="warning" showIcon message="结果被截断，请用关键词或标签缩小范围" />
          )}

          <List
            loading={loading}
            dataSource={skills}
            locale={{
              emptyText: (
                <Empty
                  description={
                    canCurate ? '目录还是空的。用「从 GitHub 纳管」放进来第一个。' : '没有匹配的技能。'
                  }
                />
              ),
            }}
            renderItem={(item) => (
              <List.Item
                actions={[
                  <Button
                    key="favorite"
                    type="text"
                    size="small"
                    loading={busyId === item.id}
                    icon={<Star size={16} fill={item.favorited ? 'currentColor' : 'none'} />}
                    onClick={() => void toggleFavorite(item)}
                  >
                    {item.favorited ? '已收藏' : '收藏'}
                  </Button>,
                  ...(canCurate
                    ? [
                        <Button
                          key="sync"
                          type="text"
                          size="small"
                          disabled={busyId !== null}
                          onClick={() => void runAction(item.id, () => skillApi.resyncSkill(item.id))}
                        >
                          同步
                        </Button>,
                        <Popconfirm
                          key="delete"
                          title="删除这个技能？"
                          description="版本、标签、收藏与使用记录一起消失，不可撤销。"
                          okText="删除"
                          okButtonProps={{ danger: true }}
                          onConfirm={() => void runAction(item.id, () => skillApi.deleteSkill(item.id))}
                        >
                          <Button type="text" size="small" danger icon={<Trash2 size={16} />} />
                        </Popconfirm>,
                      ]
                    : []),
                ]}
              >
                <List.Item.Meta
                  title={<Link to={`/skills/${item.id}`}>{item.title}</Link>}
                  description={
                    <Space direction="vertical" size={4}>
                      <Typography.Text type="secondary">{item.summary}</Typography.Text>
                      <Space wrap size={4}>
                        {item.tags.map((tag) => (
                          <Tag key={tag}>{tag}</Tag>
                        ))}
                        <Typography.Text type="secondary">
                          {item.fileCount} 个文件
                          {item.usage !== undefined && item.usage.useDays > 0
                            ? ` · 最近 30 天：${item.usage.useDays} 个人日 / ${item.usage.userCount} 人`
                            : ''}
                        </Typography.Text>
                      </Space>
                    </Space>
                  }
                />
              </List.Item>
            )}
          />
        </Space>
      </Card>

      <AppModal
        title="从 GitHub 纳管技能"
        open={importOpen}
        onCancel={() => setImportOpen(false)}
        onOk={() => void submitImport()}
        confirmLoading={importing}
        okText="纳管"
      >
        <Form form={form} layout="vertical" preserve={false}>
          <Form.Item
            name="repositoryUrl"
            label="仓库地址"
            rules={[{ required: true, message: '请给出 https://github.com/<owner>/<repo>' }]}
            extra="只接受仓库根地址。网页上看来的 /tree/… 地址请把引用与子路径拆到下面两项。"
          >
            <Input placeholder="https://github.com/yanliudesign/mono-color-skill" />
          </Form.Item>
          <Form.Item name="ref" label="引用" extra="分支 / 标签 / 提交；留空表示仓库的默认分支（推荐给分支）。">
            <Input placeholder="main" />
          </Form.Item>
          <Form.Item name="subPath" label="子路径" extra="技能包在仓库里的位置；留空表示仓库根。">
            <Input placeholder="skills/mono-color" />
          </Form.Item>
          <Form.Item name="title" label="标题" extra="留空则用 SKILL.md 里的 name。">
            <Input placeholder="留空即回退" />
          </Form.Item>
          <Form.Item name="summary" label="简介" extra="留空则用 SKILL.md 里的 description。">
            <Input placeholder="留空即回退" />
          </Form.Item>
          <Form.Item name="tags" label="标签" extra="平台内的自由字符串，用来归类与筛选。">
            <Select mode="tags" placeholder="回车添加" />
          </Form.Item>
          {capabilities !== null && (
            <Typography.Paragraph type="secondary">
              上限：{capabilities.maxFiles} 个文件、单文件 {Math.round(capabilities.maxFileBytes / 1024)} KiB、
              合计 {Math.round(capabilities.maxPackageBytes / 1024)} KiB。
            </Typography.Paragraph>
          )}
          {importError !== null && <Alert type="error" showIcon message={importError} />}
        </Form>
      </AppModal>
    </Space>
  )
}
