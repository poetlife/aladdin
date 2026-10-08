import { useCallback, useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Empty,
  Flex,
  Form,
  Input,
  List,
  Popconfirm,
  Select,
  Space,
  Tag,
  Typography,
  theme,
} from 'antd'
import { ArrowLeft, CloudDownload, RefreshCw, Trash2 } from 'lucide-react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import * as skillApi from '../../api/skill'
import { messageOf } from '../../api/errors'
import { PermissionGate } from '../../auth'
import { describeBytes } from '../../format/bytes'
import { PermissionCodes } from '../../gen/permission-codes'
import type { Skill, SkillCapabilities, SkillVersion } from '../../gen/proto/aladdin/skill/v1/skill_pb'
import { MONOSPACE } from '../../theme/monospace'
import { AppModal } from '../../ui/AppModal'
import { formatTime } from '../galaxy/format-time'
import { SkillGallery } from './SkillGallery'

/** SKILL.md 是包契约要求的那份清单文件，也是取用时默认要读的那一份。 */
const MANIFEST_PATH = 'SKILL.md'

interface MetadataFormValues {
  title?: string
  summary?: string
  tags?: string[]
}

/**
 * 一个技能的详情。
 *
 * 三件事在这一页：**它是什么**（说明层、触发说明、来源）、**它有哪些文件**、
 * **它的正文长什么样**。正文用等宽纯文本显示、**不渲染 markdown**：这份内容是
 * 写给 agent 读的，渲染成页面反而会把它读起来的样子改掉（见
 * docs/design/skill/agent-access.md）。
 *
 * 取用在这里被点出来：点开一份文件就计一次使用——"翻一翻"与"用一下"是两件事，
 * 因此不在进页面时预取正文。
 */
export function SkillDetailPage(): React.ReactNode {
  const params = useParams<{ skillId: string }>()
  const navigate = useNavigate()
  const skillId = params.skillId ?? ''

  const [form] = Form.useForm<MetadataFormValues>()
  const { token } = theme.useToken()
  const [skill, setSkill] = useState<Skill | null>(null)
  const [versions, setVersions] = useState<SkillVersion[]>([])
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [content, setContent] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const [editOpen, setEditOpen] = useState(false)
  // 图集的上限（能放几张、单张多大、合计多大）不在前端写死：它随能力一起下发。
  // 还没读到就是 null，画廊那一句说明据此不渲染。
  const [capabilities, setCapabilities] = useState<SkillCapabilities | null>(null)

  const load = useCallback(async () => {
    if (skillId === '') {
      return
    }
    setLoading(true)
    try {
      const detail = await skillApi.getSkill(skillId)
      setSkill(detail.skill ?? null)
      setVersions((await skillApi.listSkillVersions(skillId)).versions)
      setFailure(null)
    } catch (err) {
      setFailure(messageOf(err))
    } finally {
      setLoading(false)
    }
  }, [skillId])

  useEffect(() => {
    void load()
  }, [load])

  // 能力来自服务端，前端不猜（与目录页同一段写法、同一个入口）：图集上限是部署形态
  // 的公开事实，由 GetCapabilities 下发。拿不到就不下结论——真要加图时服务端会给出
  // 真实原因，而不是在这里编一个上限。
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
        // 读不到能力不等于目录不可用：这一页的主体现在照常呈现。
      })
    return () => {
      cancelled = true
    }
  }, [])

  async function openFile(path: string): Promise<void> {
    setSelectedPath(path)
    setContent(null)
    try {
      const file = await skillApi.getSkillFile(skillId, path)
      setContent(new TextDecoder().decode(file.content))
      // 取用计一次使用，因此读完之后把使用量刷新一下——否则"我读了一次"与
      // 屏幕上的数字对不上，而那正是这页唯一能给出的反馈。
      await load()
    } catch (err) {
      setFailure(messageOf(err))
    }
  }

  async function runAction(action: () => Promise<unknown>): Promise<void> {
    setBusy(true)
    try {
      await action()
      await load()
      setFailure(null)
    } catch (err) {
      setFailure(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  if (skill === null) {
    return (
      <Card loading={loading}>
        {failure !== null && <Alert type="error" showIcon message={failure} />}
      </Card>
    )
  }

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space align="center">
            {/*
             * `display: flex` 不是装饰，`inline-flex` 也不行：`<a>` 只要还是**行内**级
             * 的盒子，就按基线落座，而行盒在基线下面还留着一截下沉空间——图标于是
             * 坐在文字中线的上方约 2px。立成块级之后它的高度就是图标的高度，外层
             * Space 才是在两个等高的小盒之间居中，那条线才对得上。
             *
             * 工作台顶栏的返回用的是 antd Button（它自带对齐），这里是个纯链接，
             * 因此这一条得自己写。
             */}
            <Link to="/skills" aria-label="返回技能目录" style={{ display: 'flex' }}>
              <ArrowLeft size={16} />
            </Link>
            {skill.title}
          </Space>
        }
        extra={
          /*
           * 页头只放**与"这一页"这个整体有关**的两颗：刷新（重新读一遍）与同步
           * （追远端）。其余动作各回各的块——加图与设为首图挨着画廊、改说明层挨着
           * 说明层、删除收成图标。
           *
           * 六颗平铺在页头时，问题不只是挤：标题被压成几个字，而且**看不出哪一颗
           * 在动哪一块**（见 docs/design/uiux/README.md 的"信息层级"）。
           */
          <Space>
            <Button icon={<RefreshCw size={16} />} onClick={() => void load()}>
              刷新
            </Button>
            <PermissionGate require={PermissionCodes.SkillCatalogWrite}>
              <Button
                icon={<CloudDownload size={16} />}
                disabled={busy}
                onClick={() => void runAction(() => skillApi.resyncSkill(skill.id))}
              >
                同步
              </Button>
              {/* 破坏性操作按 uiux 的规矩**默认不突出**：收到只剩一个图标，确认框
                  里说明后果。 */}
              <Popconfirm
                title="删除这个技能？"
                description="版本、标签、收藏与使用记录一起消失，不可撤销。"
                okText="删除"
                okButtonProps={{ danger: true }}
                onConfirm={() =>
                  void runAction(async () => {
                    await skillApi.deleteSkill(skill.id)
                    navigate('/skills')
                  })
                }
              >
                <Button type="text" danger icon={<Trash2 size={16} />} disabled={busy} />
              </Popconfirm>
            </PermissionGate>
          </Space>
        }
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          {failure !== null && <Alert type="error" showIcon message={failure} />}

          {/* 画廊与说明**并排**、放不下就换行（`wrap`，与断点无关的写法，见
              docs/design/web/responsive.md）。展示图集是说明层的一项、**不是包的
              内容**，所以它在说明这一块里，不混进下面那份文件清单。 */}
          <Flex wrap gap={24} align="flex-start">
            <div style={{ flex: '1 1 360px', minWidth: 0 }}>
              <SkillGallery
                skill={skill}
                busy={busy}
                runAction={runAction}
                maxImages={capabilities?.maxImages}
                maxImageBytes={capabilities?.maxImageBytes}
                maxImageTotalBytes={capabilities?.maxImageTotalBytes}
              />
            </div>
            <div style={{ flex: '1 1 320px', minWidth: 0 }}>
              <Typography.Paragraph>{skill.summary}</Typography.Paragraph>
              <Descriptions size="small" column={2}>
                <Descriptions.Item label="触发说明" span={2}>
                  {skill.description}
                </Descriptions.Item>
                <Descriptions.Item label="标签" span={2}>
                  {skill.tags.length === 0 ? '—' : skill.tags.map((tag) => <Tag key={tag}>{tag}</Tag>)}
                </Descriptions.Item>
                <Descriptions.Item label="来源" span={2}>
                  {skill.source?.repositoryUrl}
                  {skill.source?.ref !== '' ? ` @ ${skill.source?.ref}` : ''}
                  {skill.source?.subPath !== '' ? ` / ${skill.source?.subPath}` : ''}
                  <Typography.Text type="secondary">
                    {' '}
                    （提交 {skill.source?.commit.slice(0, 12)}）
                  </Typography.Text>
                </Descriptions.Item>
                <Descriptions.Item label="当前版本">
                  {skill.currentVersionId}
                  <Typography.Text type="secondary">
                    {' '}
                    {formatTime(skill.currentVersionCreatedAt)}
                  </Typography.Text>
                </Descriptions.Item>
                <Descriptions.Item label="使用量">
                  {skill.usage !== undefined && skill.usage.useDays > 0
                    ? `最近 30 天：${skill.usage.useDays} 个人日 / ${skill.usage.userCount} 人`
                    : '最近 30 天没有人取用过'}
                </Descriptions.Item>
              </Descriptions>
            </div>
          </Flex>

          {/* 改说明层**在这一块里**：它改的是上面那几项文字，放到页头只会让"哪一颗
              在动哪一块"变成一个要猜的问题。图集的那几颗跟着画廊走（见
              SkillGallery），因此这一排只剩改说明层。 */}
          <PermissionGate require={PermissionCodes.SkillCatalogWrite}>
            <Space wrap size={4} style={{ borderTop: `1px solid ${token.colorSplit}`, paddingTop: 12 }}>
              <Button
                type="text"
                size="small"
                disabled={busy}
                onClick={() => {
                  form.setFieldsValue({
                    title: skill.titleOverride,
                    summary: skill.summaryOverride,
                    tags: skill.tags,
                  })
                  setEditOpen(true)
                }}
              >
                改说明层
              </Button>
            </Space>
          </PermissionGate>
        </Space>
      </Card>

      <Card title="文件与正文" size="small">
        <Space align="start" size="middle" style={{ width: '100%' }}>
          <List
            size="small"
            style={{ width: 280, flex: '0 0 auto' }}
            dataSource={skill.files}
            locale={{ emptyText: <Empty description="没有文件" /> }}
            renderItem={(file) => (
              <List.Item
                style={{
                  cursor: 'pointer',
                  background: file.path === selectedPath ? 'rgba(0,0,0,0.04)' : undefined,
                }}
                onClick={() => void openFile(file.path)}
              >
                <Typography.Text>{file.path}</Typography.Text>
                <Typography.Text type="secondary">{describeBytes(file.sizeBytes)}</Typography.Text>
              </List.Item>
            )}
          />
          <div style={{ flex: 1, minWidth: 0 }}>
            {selectedPath === null ? (
              <Empty description={`点左边的一份文件读它的正文。${MANIFEST_PATH} 是这个技能的主入口。`} />
            ) : (
              <pre
                style={{
                  fontFamily: MONOSPACE,
                  fontSize: 12,
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                  margin: 0,
                  maxHeight: 520,
                  overflow: 'auto',
                }}
              >
                {content ?? '读取中…'}
              </pre>
            )}
          </div>
        </Space>
      </Card>

      <PermissionGate require={PermissionCodes.SkillCatalogWrite}>
        <Card title="版本" size="small">
          <List
            size="small"
            dataSource={versions}
            renderItem={(version) => (
              <List.Item
                actions={[
                  version.current ? (
                    <Tag key="current" color="green">
                      当前
                    </Tag>
                  ) : (
                    <Button
                      key="switch"
                      type="link"
                      size="small"
                      disabled={busy}
                      onClick={() =>
                        void runAction(() => skillApi.setCurrentVersion(skill.id, version.id))
                      }
                    >
                      切到这一版
                    </Button>
                  ),
                ]}
              >
                <Space>
                  <Typography.Text code>{version.id}</Typography.Text>
                  <Typography.Text type="secondary">
                    {version.commit.slice(0, 12)} · {version.fileCount} 个文件
                    {version.skippedFiles > 0 ? ` · 上游另有 ${version.skippedFiles} 个条目未收` : ''}
                    {formatTime(version.createdAt)}
                  </Typography.Text>
                </Space>
              </List.Item>
            )}
          />
          <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
            回滚只切指针、不改任何字节。<Typography.Text strong>回滚之后再同步不会把指针挪回去</Typography.Text>
            ——同步只在远端有新提交时动指针，否则一次习惯性的同步就会悄悄撤销一次刚做的回滚。
          </Typography.Paragraph>
        </Card>
      </PermissionGate>

      <AppModal
        title="改说明层"
        open={editOpen}
        onCancel={() => setEditOpen(false)}
        confirmLoading={busy}
        onOk={() =>
          void form.validateFields().then(async (values) => {
            await runAction(() =>
              skillApi.updateMetadata(skill.id, values.title ?? '', values.summary ?? '', values.tags ?? []),
            )
            setEditOpen(false)
          })
        }
      >
        <Form form={form} layout="vertical" preserve={false}>
          <Form.Item name="title" label="标题" extra="留空即回到 SKILL.md 里的 name。">
            <Input placeholder={`回退到 ${skill.name}`} />
          </Form.Item>
          <Form.Item name="summary" label="简介" extra="留空即回到 SKILL.md 里的 description。">
            <Input placeholder="留空即回退" />
          </Form.Item>
          <Form.Item name="tags" label="标签" extra="整体替换：不给表示去掉全部标签。">
            <Select mode="tags" />
          </Form.Item>
        </Form>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          改说明层<Typography.Text strong>不改内容层任何一项</Typography.Text>：当前版本、文件清单与所有字节逐字不变。
        </Typography.Paragraph>
      </AppModal>
    </Space>
  )
}
