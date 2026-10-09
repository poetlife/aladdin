import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Card, Descriptions, Skeleton, Space, Tag, Typography } from 'antd'
import { RefreshCw, ServerCog } from 'lucide-react'

import * as opsApi from '../../api/ops'
import { messageOf, traceIdOf } from '../../api/errors'
import { useSession } from '../../auth'
import { buildInfo, compareCommits } from '../../build/build-info'
import { describeDuration } from '../../format/duration'
import type { GetDeploymentInfoResponse } from '../../gen/proto/aladdin/ops/v1/ops_pb'

/**
 * 运行时长本地刷新的间隔。
 *
 * **不轮询接口**：uptime 是"启动时刻"的一个纯函数，界面自己按它算就行。为它反复
 * 拉取会让一个只读页面变成一台定时打服务端的机器，而这个数字一点也不比"上一次
 * 读取时的 uptime 加上了流逝的时间"更准。
 */
const UPTIME_TICK_MS = 60_000

// failure 是一次失败的展示信息：给用户的文案，以及可拿去找日志的追踪 ID。
interface failure {
  message: string
  traceId: string | null
}

/**
 * 一个可能为空的取值。
 *
 * 空时的文案由调用方给，因为**"没有"有好几种**：构建期没注入是"未注入"，主机名
 * 取不到是"取不到"。写死一种会让另一处说假话。
 */
function MaybeText({
  value,
  empty,
  copyable = false,
}: {
  value: string
  empty: string
  copyable?: boolean
}): React.ReactNode {
  if (value === '') {
    return <Typography.Text type="secondary">{empty}</Typography.Text>
  }
  // copyable 只在真给的时候展开：仓库开了 exactOptionalPropertyTypes，显式传一个
  // undefined 不是"没给"，antd 的 TextProps 也不接受它。
  return (
    <Typography.Text code={copyable} {...(copyable ? { copyable: true } : {})}>
      {value}
    </Typography.Text>
  )
}

/** 一个时刻：RFC3339 转本地时间；空表示构建期没有注入。 */
function MaybeMoment({ value }: { value: string }): React.ReactNode {
  if (value === '') {
    return <Typography.Text type="secondary">未注入</Typography.Text>
  }
  return <>{new Date(value).toLocaleString()}</>
}

/**
 * 部署信息页。
 *
 * 它回答运维与发版核对时最先被问的三个问题：**线上跑的是哪个版本**、**前后端是不是
 * 同一次构建**、**这个进程起来多久了、是哪一个副本**。这些问题原先要 ssh 上去问。
 *
 * 两条边界写在页面上：
 *
 *   - **它不讲配置**。库地址、桶地址、密钥一概没有——版本信息是"哪次构建"，配置是
 *     "这份部署怎么配的"，而且后者可能含密钥（见 docs/design/config/README.md）。
 *   - **运行时长本地算**。服务端给的是启动时刻，展示由本页按它推。因此刷新按钮
 *     换的是"服务端的自述"，不是"现在几点"。
 *
 * 页面上的"前端"那一侧取自构建期注入（见 web/src/build/build-info.ts），与后端的
 * 取值出自同一处 VERSION / COMMIT / BUILD_TIME——因此两栏对不上是一个真结论。
 */
export function DeploymentPage(): React.ReactNode {
  const { scope } = useSession()
  const [info, setInfo] = useState<GetDeploymentInfoResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<failure | null>(null)
  // 只看得到"现在几点"这一件事的本地状态。它每分钟走一格，uptime 由它与
  // 服务端给的启动时刻算出来（见 UPTIME_TICK_MS）。
  const [now, setNow] = useState<number>(() => Date.now())

  const load = useCallback(async (): Promise<void> => {
    setLoading(true)
    setFailure(null)
    try {
      const response = await opsApi.getDeploymentInfo(scope)
      setInfo(response)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      setInfo(null)
    } finally {
      setLoading(false)
    }
  }, [scope])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), UPTIME_TICK_MS)
    return () => clearInterval(timer)
  }, [])

  const commitAgreement = compareCommits(buildInfo.commit, info?.commit ?? '')

  return (
    <Card
      title={
        <Space size={8}>
          <ServerCog size={16} />
          部署信息
        </Space>
      }
      extra={
        <Button icon={<RefreshCw size={16} />} loading={loading} onClick={() => void load()}>
          刷新
        </Button>
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

      <Typography.Paragraph type="secondary">
        这一页只描述<Typography.Text strong>构建与进程</Typography.Text>
        ：哪一次构建、跑在什么平台上、起来了多久。配置取值（库、对象存储、登录渠道）
        不在这里——那些可能含密钥。运行时长由本页按启动时刻本地推算，不轮询接口。
      </Typography.Paragraph>

      {/* 两栏提交号都对得上时才不占地方；对不上或无从比较时都在这里表个态。 */}
      {commitAgreement === 'different' && (
        <Alert
          type="warning"
          showIcon
          title="前端与后端的提交号不一致"
          description={
            <Space direction="vertical" size={2}>
              <span>
                前端 {buildInfo.commit} ／ 后端 {info?.commit ?? ''}
              </span>
              {/* 两种成因的处置方向不同，所以两种都说：只看现象分不出是哪一种。 */}
              <span>
                常见成因是浏览器缓存了旧的 bundle（强制刷新即可），或这个部署的前后端是分开发布的。
              </span>
            </Space>
          }
          style={{ marginBottom: 16 }}
        />
      )}
      {commitAgreement === 'unknown' && (
        <Alert
          type="info"
          showIcon
          title="无从比较前后端的提交号"
          description="至少一侧的构建没有注入提交号（本机开发构建、或未经 make 的构建）。这一栏只在两侧都有值时才有结论。"
          style={{ marginBottom: 16 }}
        />
      )}

      {loading && info === null ? (
        <Skeleton active />
      ) : (
        <>
          <Typography.Title level={5}>后端（这个进程）</Typography.Title>
          <Descriptions column={{ xs: 1, sm: 1, md: 2 }} size="small" bordered>
            <Descriptions.Item label="版本">
              <MaybeText value={info?.version ?? ''} empty="未知" copyable />
            </Descriptions.Item>
            <Descriptions.Item label="构建方式">
              {info === null ? (
                '—'
              ) : info.released ? (
                // 发布产物才能自更新，因此这一栏直接看着它说（见 internal/upgrade）。
                <Tag color="green">发布产物</Tag>
              ) : (
                <Tag>本机构建</Tag>
              )}
            </Descriptions.Item>
            <Descriptions.Item label="提交号">
              <MaybeText value={info?.commit ?? ''} empty="未注入" copyable />
            </Descriptions.Item>
            <Descriptions.Item label="构建时间">
              <MaybeMoment value={info?.builtAt ?? ''} />
            </Descriptions.Item>
            <Descriptions.Item label="Go 版本">
              <MaybeText value={info?.goVersion ?? ''} empty="未知" />
            </Descriptions.Item>
            <Descriptions.Item label="平台">
              {info === null || info.os === '' ? (
                '—'
              ) : (
                <Typography.Text code>
                  {info.os}/{info.arch}
                </Typography.Text>
              )}
            </Descriptions.Item>
            <Descriptions.Item label="实例">
              {/* 多副本部署时，这一项就是"我看的是哪一个副本"。取不到主机名的极简
                  容器里会是空的——那时留空，不编一个随机标识。 */}
              <MaybeText value={info?.instance ?? ''} empty="取不到主机名" copyable />
            </Descriptions.Item>
            <Descriptions.Item label="启动时间">
              <MaybeMoment value={info?.startedAt ?? ''} />
            </Descriptions.Item>
            <Descriptions.Item label="运行时长">
              {info === null || info.startedAt === '' ? (
                '—'
              ) : (
                // 时钟偏差会算出一个负数，describeDuration 把它显示成「—」而不是
                // 一个负的时长（见 format/duration.ts）。
                describeDuration((now - Date.parse(info.startedAt)) / 1000)
              )}
            </Descriptions.Item>
          </Descriptions>

          <Typography.Title level={5} style={{ marginTop: 24 }}>
            前端（这个页面）
          </Typography.Title>
          <Descriptions column={{ xs: 1, sm: 1, md: 2 }} size="small" bordered>
            <Descriptions.Item label="版本">
              <MaybeText value={buildInfo.version} empty="未知" copyable />
            </Descriptions.Item>
            <Descriptions.Item label="提交号">
              <MaybeText value={buildInfo.commit} empty="未注入" copyable />
            </Descriptions.Item>
            <Descriptions.Item label="构建时间">
              <MaybeMoment value={buildInfo.buildTime} />
            </Descriptions.Item>
          </Descriptions>
          <Typography.Paragraph type="secondary" style={{ marginTop: 8 }}>
            这一栏读的是<Typography.Text strong>浏览器里正在跑的那份 bundle</Typography.Text>
            ：它与后端取自同一次构建的同一组值。因此显示的是"你打开的这份页面"，而不是
            "服务器上现在放着的那份"——页面缓存过的话，两者不是一回事。
          </Typography.Paragraph>
        </>
      )}
    </Card>
  )
}
