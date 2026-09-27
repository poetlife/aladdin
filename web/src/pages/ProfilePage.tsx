import { useEffect, useState } from 'react'
import { Alert, Avatar, Button, Card, Divider, Form, Input, Space, Typography, Upload } from 'antd'
import { CircleUserRound, ImageUp, Info, Pencil, Trash2 } from 'lucide-react'

import { messageOf } from '../api/errors'
import { useSession } from '../auth'
import { IdentityCard } from '../identity/identity-card'
import { avatarFallbackInitial, useProfile } from '../profile'

interface ProfileFormValues {
  nickname?: string
  bio?: string
}

/**
 * 个人资料页。
 *
 * 它是**自服务**页面：每个动作都只作用于当前主体，因此不需要任何权限码，
 * 也不做权限裁剪——零权限的主体同样要能给自己起个名字
 * （见 docs/design/profile/README.md）。
 *
 * 页面不自己拼展示名、不自己拼头像地址、也不判断头像类型：那些都由服务端
 * 定，前端只渲染。
 */
export function ProfilePage(): React.ReactNode {
  const { profile, loading, error, reload, updateProfile, updateAvatar, deleteAvatar } = useProfile()
  // 主体标识取自会话，不从档案里再取一份：那是认证面的数据，档案里没有它，
  // 也不该有——同一份事实两个来源必然漂移。
  const { subject } = useSession()
  const [form] = Form.useForm<ProfileFormValues>()

  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const [avatarBusy, setAvatarBusy] = useState(false)
  const [avatarError, setAvatarError] = useState<string | null>(null)

  // 进这一页时重取一次：档案在别处可能已经变了；而头像是**短时地址**，
  // 上一次读到的那个可能已经过期（见 docs/design/profile/avatar-storage.md）。
  useEffect(() => {
    void reload()
  }, [reload])

  // 表单初值来自服务端，而不是空的本地状态：档案是异步到达的，用
  // initialValues 拿不到它。每次档案更新都重置一次（含保存之后），
  // 这样输入框里显示的永远是与页头一致的、服务端那一份。
  useEffect(() => {
    if (profile !== null) {
      form.setFieldsValue({ nickname: profile.nickname, bio: profile.bio })
    }
  }, [profile, form])

  async function handleSave(values: ProfileFormValues): Promise<void> {
    setSaving(true)
    setSaveError(null)
    setSaved(false)
    try {
      await updateProfile(values.nickname ?? '', values.bio ?? '')
      setSaved(true)
    } catch (err) {
      setSaveError(messageOf(err))
    } finally {
      setSaving(false)
    }
  }

  async function handleAvatarFile(file: File): Promise<void> {
    // 早退检查用的是**服务端下发的上限**，不在前端另写一份：写死一份就会与
    // 服务端漂移，而漂移的表现是"前端放行、上传之后被拒"。
    //
    // 它只是省一次往返，**不是校验**：真正的边界在服务端，那里对字节做嗅探
    // 与大小判定（见 docs/design/profile/avatar-storage.md）。
    if (profile !== null && file.size > profile.avatarMaxBytes) {
      setAvatarError(`头像不能超过 ${describeBytes(profile.avatarMaxBytes)}`)
      return
    }

    setAvatarBusy(true)
    setAvatarError(null)
    try {
      await updateAvatar(new Uint8Array(await file.arrayBuffer()))
    } catch (err) {
      setAvatarError(messageOf(err))
    } finally {
      setAvatarBusy(false)
    }
  }

  async function handleDeleteAvatar(): Promise<void> {
    setAvatarBusy(true)
    setAvatarError(null)
    try {
      await deleteAvatar()
    } catch (err) {
      setAvatarError(messageOf(err))
    } finally {
      setAvatarBusy(false)
    }
  }

  if (loading && profile === null) {
    return (
      <Card>
        <Typography.Text type="secondary">正在读取档案…</Typography.Text>
      </Card>
    )
  }

  // 读不到档案时仍给出说明，而不是一片空白：页头此时显示的是主体标识，
  // 用户需要知道那是"取不到名字时的兜底"，不是他的名字。
  if (profile === null) {
    return (
      <Card title="个人资料">
        <Alert
          type="error"
          showIcon
          message="读取档案失败"
          description={error ?? '请稍后重试'}
          action={<Button onClick={() => void reload()}>重试</Button>}
        />
      </Card>
    )
  }

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <CircleUserRound size={16} />
            头像
          </Space>
        }
      >
        {avatarError !== null && (
          <Alert type="error" message={avatarError} style={{ marginBottom: 16 }} />
        )}
        <Space size={24} align="center">
          <Avatar size={96} src={profile.avatarUrl === '' ? undefined : profile.avatarUrl}>
            {avatarFallbackInitial(profile.displayName)}
          </Avatar>

          {profile.avatarUploadEnabled ? (
            <Space orientation="vertical" size={8}>
              <Space>
                <Upload
                  accept="image/png,image/jpeg,image/gif"
                  showUploadList={false}
                  // 返回 false 阻止 antd 自己发起上传：它会把文件 PUT 到一个
                  // 我们没配的地址。上传走 RPC，类型由服务端判定。
                  beforeUpload={(file) => {
                    void handleAvatarFile(file)
                    return false
                  }}
                >
                  <Button icon={<ImageUp size={16} />} loading={avatarBusy}>
                    上传头像
                  </Button>
                </Upload>
                {profile.avatarUrl !== '' && (
                  <Button
                    danger
                    icon={<Trash2 size={16} />}
                    loading={avatarBusy}
                    onClick={() => void handleDeleteAvatar()}
                  >
                    删除头像
                  </Button>
                )}
              </Space>
              <Typography.Text type="secondary">
                支持 PNG / JPEG / GIF，不超过 {describeBytes(profile.avatarMaxBytes)}。
              </Typography.Text>
            </Space>
          ) : (
            <Typography.Text type="secondary">
              本部署未启用头像功能。昵称与简介照常可用。
            </Typography.Text>
          )}
        </Space>
      </Card>

      <Card
        title={
          <Space size={8}>
            <Pencil size={16} />
            昵称与简介
          </Space>
        }
      >
        {saveError !== null && (
          <Alert type="error" message={saveError} style={{ marginBottom: 16 }} />
        )}
        {saved && <Alert type="success" message="已保存" style={{ marginBottom: 16 }} />}

        <Form<ProfileFormValues>
          form={form}
          layout="vertical"
          onFinish={(values) => void handleSave(values)}
          onValuesChange={() => setSaved(false)}
        >
          <Form.Item name="nickname" label="昵称" extra="留空则显示你的登录渠道标识">
            <Input maxLength={32} placeholder="比如：阿拉丁" autoComplete="off" />
          </Form.Item>
          <Form.Item name="bio" label="简介" extra="留空则显示为空">
            <Input.TextArea maxLength={280} rows={3} placeholder="随便写点什么" />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={saving}>
            保存
          </Button>
        </Form>
      </Card>

      <IdentityCard />

      <Card>
        <Space align="start" size={8}>
          <Info size={16} style={{ marginTop: 4, flexShrink: 0 }} />
          <div>
            <Typography.Text type="secondary">
              界面上的名字与头像由服务端算好下发：未设置昵称时回退到登录渠道标识。
              档案
              <Typography.Text strong>不参与任何权限判定</Typography.Text>
              ，改它不会改变你能做什么。
            </Typography.Text>
            <Divider style={{ margin: '12px 0' }} />
            <Typography.Text type="secondary">主体标识：{subject?.subjectId ?? '—'}</Typography.Text>
          </div>
        </Space>
      </Card>
    </Space>
  )
}

/**
 * 把字节数说成人话，用于提示文案。
 */
function describeBytes(bytes: number): string {
  const mib = bytes / (1024 * 1024)
  if (mib >= 1) {
    return `${Number.isInteger(mib) ? mib : mib.toFixed(1)} MiB`
  }
  return `${Math.round(bytes / 1024)} KB`
}
