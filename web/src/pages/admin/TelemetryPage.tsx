import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Card, Empty, Segmented, Space, Table, Tag, Tooltip, Typography } from 'antd'
import type { TableProps } from 'antd'
import { Activity, RefreshCw } from 'lucide-react'

import * as telemetryAdminApi from '../../api/telemetry-admin'
import { messageOf, traceIdOf } from '../../api/errors'
import { useSession } from '../../auth'
import type { EventStat, RecentEvent } from '../../gen/proto/aladdin/telemetry/v1/telemetry_admin_pb'
import { TimeWindow } from '../../gen/proto/aladdin/telemetry/v1/telemetry_admin_pb'
import { Client, Result, Surface } from '../../gen/proto/aladdin/telemetry/v1/telemetry_pb'

// 枚举取值到界面文案的映射。
//
// 未登记时回落到枚举名而不是空白：读侧拿到的是**历史数据**，某个取值今天没被
// 登记（例如清单已删掉一个动作）也不该在页面上变成一片空白——那看起来像"这一列
// 没有值"，而不是"这是一个不认识的取值"。
const CLIENT_LABELS: Record<number, string> = {
  [Client.WEB]: 'Web',
  [Client.CLI]: '命令行',
}

const SURFACE_LABELS: Record<number, string> = {
  [Surface.WEB_AUTH]: '登录页',
  [Surface.WEB_PROJECT_LIST]: '工程列表',
  [Surface.WEB_EDITOR]: '工作台',
  [Surface.WEB_PREVIEW]: '预览',
  [Surface.CLI]: '命令行',
}

const RESULT_LABELS: Record<number, string> = {
  [Result.OK]: '成功',
  [Result.FAIL]: '失败',
  [Result.CANCEL]: '取消',
  [Result.BLOCKED]: '被拦下',
}

// 结局的颜色只承担一个作用：让"失败/被拦下"在一屏里跳出来。
// `blocked` 与 `fail` 分开着色，因为它们的修复方向不同——前者是前端拦下的，
// 后者是执行了但没成功。
const RESULT_COLORS: Record<number, string> = {
  [Result.OK]: 'green',
  [Result.FAIL]: 'red',
  [Result.CANCEL]: 'default',
  [Result.BLOCKED]: 'orange',
}

function resultColor(result: Result): string {
  return RESULT_COLORS[result] ?? 'default'
}

const WINDOW_OPTIONS = [
  { value: TimeWindow.TODAY, label: '今日' },
  { value: TimeWindow.LAST_7_DAYS, label: '近 7 天' },
  { value: TimeWindow.LAST_30_DAYS, label: '近 30 天' },
]

function labelOf(labels: Record<number, string>, value: number, fallback: string): string {
  return labels[value] ?? fallback
}

const statColumns: NonNullable<TableProps<EventStat>['columns']> = [
  {
    title: '上报端',
    dataIndex: 'client',
    key: 'client',
    render: (client: Client) => labelOf(CLIENT_LABELS, client, `未知端 ${client}`),
  },
  {
    title: '动作',
    dataIndex: 'action',
    key: 'action',
    render: (action: string) => <Typography.Text code>{action}</Typography.Text>,
  },
  {
    title: '结局',
    dataIndex: 'result',
    key: 'result',
    render: (result: Result) => (
      <Tag color={resultColor(result)}>{labelOf(RESULT_LABELS, result, `未知结局 ${result}`)}</Tag>
    ),
  },
  {
    // int64 在生成类型里是 bigint（与 galaxy 的 seq 同理），转成数字再展示。
    title: '次数',
    dataIndex: 'count',
    key: 'count',
    render: (count: bigint) => Number(count),
  },
]

const eventColumns: NonNullable<TableProps<RecentEvent>['columns']> = [
  {
    title: '时间',
    dataIndex: 'occurredAt',
    key: 'occurredAt',
    render: (occurredAt: string) => new Date(occurredAt).toLocaleString(),
  },
  {
    title: '上报端',
    dataIndex: 'client',
    key: 'client',
    render: (client: Client) => labelOf(CLIENT_LABELS, client, `未知端 ${client}`),
  },
  {
    title: '界面',
    dataIndex: 'surface',
    key: 'surface',
    render: (surface: Surface) => labelOf(SURFACE_LABELS, surface, `未知界面 ${surface}`),
  },
  {
    title: '动作',
    dataIndex: 'action',
    key: 'action',
    render: (action: string) => <Typography.Text code>{action}</Typography.Text>,
  },
  {
    title: '结局',
    dataIndex: 'result',
    key: 'result',
    render: (result: Result) => (
      <Tag color={resultColor(result)}>{labelOf(RESULT_LABELS, result, `未知结局 ${result}`)}</Tag>
    ),
  },
  {
    title: '耗时',
    dataIndex: 'durationMs',
    key: 'durationMs',
    // 0 表示上报端未提供，而不是"耗时为零"——proto3 无法区分这两者，
    // 约定是 0 一律按未提供（见 telemetry.proto）。
    render: (durationMs: number) =>
      durationMs > 0 ? `${durationMs} ms` : <Typography.Text type="secondary">—</Typography.Text>,
  },
  {
    title: '主体',
    dataIndex: 'subjectId',
    key: 'subjectId',
    render: (subjectId: string) =>
      subjectId !== '' ? (
        <Typography.Text code>{subjectId}</Typography.Text>
      ) : (
        <Typography.Text type="secondary">匿名</Typography.Text>
      ),
  },
  {
    title: '属性',
    dataIndex: 'attrs',
    key: 'attrs',
    render: (attrs: Record<string, string>) => {
      const entries = Object.entries(attrs)
      if (entries.length === 0) return <Typography.Text type="secondary">—</Typography.Text>
      return (
        <Space wrap size={4}>
          {entries.map(([key, value]) => (
            <Tag key={key}>
              {key}={value}
            </Tag>
          ))}
        </Space>
      )
    },
  },
  {
    // 列头带一句解释：这一列会**成片为空**，而空的原因分两种，都不是"数据丢了"
    // ——没有伴随 RPC 的动作（被前端拦下、确认框被取消、纯前端切换）本来就没有
    // 可指的链路；请求没发出去时也不该编一个。不写清楚，看的人会以为链路标识没报上来。
    title: (
      <Tooltip title="这条动作伴随的那次 RPC 的链路标识。没有发出请求的动作（被拦下、取消、纯前端切换）没有它。">
        <span>追踪 ID</span>
      </Tooltip>
    ),
    dataIndex: 'clientTraceId',
    key: 'clientTraceId',
    render: (clientTraceId: string) =>
      clientTraceId !== '' ? (
        <Typography.Text code copyable>
          {clientTraceId}
        </Typography.Text>
      ) : (
        <Typography.Text type="secondary">—</Typography.Text>
      ),
  },
]

// failure 是一次失败的展示信息：给用户的文案，以及可拿去找日志的追踪 ID。
interface failure {
  message: string
  traceId: string | null
}

/**
 * 客户端遥测页。
 *
 * 它回答两个问题：**Web/CLI 最近在做什么**（按端 × 动作 × 结局计数），以及
 * **刚才具体发生了什么**（最近一页明细）。数据来自写侧落库的客户端事件——
 * 那些"请求根本没发出去"的本地动作，服务端的请求留痕看不见它们
 * （见 docs/observability.md 的「客户端事件」）。
 *
 * 它是**只读**的：没有任何写入口，也没有任意过滤条件——时间窗是一个有界枚举，
 * 条数由服务端收敛。需要更强检索能力时请去日志系统，这一页刻意不做第二套观测平台。
 *
 * 页面上不解释"为什么这个数字是这些"以外的任何东西；字段语义以
 * docs/observability.md 为准。
 */
export function TelemetryPage(): React.ReactNode {
  const { scope } = useSession()
  const [window, setWindow] = useState<TimeWindow>(TimeWindow.LAST_7_DAYS)
  const [stats, setStats] = useState<EventStat[]>([])
  const [events, setEvents] = useState<RecentEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<failure | null>(null)

  const load = useCallback(async (): Promise<void> => {
    setLoading(true)
    setFailure(null)
    try {
      // 两张表用同一个窗口取数：计数与明细各用各的口径，会让"计数说没有失败、
      // 明细里却列着一条失败"这种自相矛盾出现。
      const [statsResponse, eventsResponse] = await Promise.all([
        telemetryAdminApi.listEventStats(scope, window),
        telemetryAdminApi.listRecentEvents(scope, window),
      ])
      setStats(statsResponse.stats)
      setEvents(eventsResponse.events)
    } catch (err) {
      setFailure({ message: messageOf(err), traceId: traceIdOf(err) })
      setStats([])
      setEvents([])
    } finally {
      setLoading(false)
    }
  }, [scope, window])

  useEffect(() => {
    void load()
  }, [load])

  // 切窗口即重新取数：窗口是这一页的主参数，不该再要求用户点一次刷新。
  const onSwitchWindow = (next: TimeWindow): void => {
    setWindow(next)
  }

  return (
    <Card
      title={
        <Space size={8}>
          <Activity size={16} />
          客户端遥测
        </Space>
      }
      extra={
        <Space size={8}>
          <Segmented<TimeWindow>
            value={window}
            onChange={onSwitchWindow}
            options={WINDOW_OPTIONS}
          />
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
      <Typography.Paragraph type="secondary">
        这里记的是客户端本地动作：前端拦下的保存、按上限早退的上传、切换预览、打开弹层，
        以及命令行在配置或凭证阶段就退出的失败。有出站请求的动作不在这里——那些由服务端的
        请求留痕覆盖。事件只保留 30 天。
      </Typography.Paragraph>

      <Typography.Title level={5}>按端 / 动作 / 结局计数</Typography.Title>
      <Table<EventStat>
        rowKey={(stat) => `${stat.client}-${stat.action}-${stat.result}`}
        columns={statColumns}
        dataSource={stats}
        loading={loading}
        pagination={false}
        // 手机上列装不下，让表格在自己的容器里横向滚动，而不是把整页顶宽。
        scroll={{ x: 'max-content' }}
        locale={{
          emptyText: (
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description="这个时间窗内没有事件。刚部署或刚上线时这是正常的。"
            />
          ),
        }}
      />

      <Typography.Title level={5} style={{ marginTop: 24 }}>
        最近事件
      </Typography.Title>
      {/* 明细与计数共用同一个窗口；这里说清它不是"全部历史"，
          免得有人把它当成一个可以往下翻的日志界面。 */}
      <Typography.Paragraph type="secondary">
        只列这个时间窗内最新的一页。事件没有自由文本字段，属性是服务端登记过的白名单键。
      </Typography.Paragraph>
      <Table<RecentEvent>
        // 事件之间**可能完全相同**（同一批里同端同动作同结局、都没有 trace_id），
        // 按字段拼 key 会撞；序号在这里是唯一可靠的区分。
        rowKey={(_, index) => index ?? 0}
        columns={eventColumns}
        dataSource={events}
        loading={loading}
        pagination={false}
        scroll={{ x: 'max-content' }}
        locale={{
          emptyText: (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="这个时间窗内没有事件" />
          ),
        }}
      />
    </Card>
  )
}
