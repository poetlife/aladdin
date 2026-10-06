import { Card, Space, Table, Typography } from 'antd'
import type { TableProps } from 'antd'

import { FRAME_CHANNEL, FRAME_CHANNEL_VERSION, FRAME_IMAGE_PREVIEW_TYPE } from '../galaxy/frame-channel'
import { CommandBlock } from './CommandBlock'

/** 信封字段表的一行。 */
interface FieldRow {
  key: string
  field: string
  type: string
  value: React.ReactNode
}

/** 消息表的一行。 */
interface MessageRow {
  key: string
  direction: string
  type: string
  payload: React.ReactNode
}

/**
 * 页面与外壳的通道。它是**接口参考**，不是介绍页。
 *
 * 写这一页时守四条（见 docs/design/web/docs-area.md）：
 *
 * - **只写事实，不写背景与理由。** 理由属于 spec（docs/design/galaxy/site-model.md
 *   的"平台接入桥"），作者在这里需要的是字段、取值、约束。
 * - **短句。一句一件事。** 一句不跨两个事实。
 * - **一个概念一个词**：文档页、外壳、消息、丢弃，通篇不变。
 * - **不出现部署实例的值**，示例里一律用 `<…>` 占位；也不出现只有登录者才有的东西
 *  （本区将来要放出去）。
 *
 * 协议取值从实现里取（`frame-channel.ts`），不手抄一份——手抄的那份会在协议改动
 * 那天不声不响地说错话，而这是对外契约。
 */
export function FrameBridgePage(): React.ReactNode {
  const sampleMessage = `{
  "channel": "${FRAME_CHANNEL}",
  "version": ${FRAME_CHANNEL_VERSION},
  "type": "${FRAME_IMAGE_PREVIEW_TYPE}",
  "payload": {
    "images": [{ "src": "<图片的绝对地址>", "alt": "甲图" }],
    "index": 0
  }
}`

  const fieldColumns: TableProps<FieldRow>['columns'] = [
    {
      title: '字段',
      dataIndex: 'field',
      key: 'field',
      render: (field: string) => <Typography.Text code>{field}</Typography.Text>,
    },
    { title: '类型', dataIndex: 'type', key: 'type' },
    { title: '取值', dataIndex: 'value', key: 'value' },
  ]

  const fields: FieldRow[] = [
    { key: 'channel', field: 'channel', type: 'string', value: <>固定为 {FRAME_CHANNEL}</> },
    { key: 'version', field: 'version', type: 'number', value: <>固定为 {FRAME_CHANNEL_VERSION}</> },
    { key: 'type', field: 'type', type: 'string', value: <>消息类型。取值见"消息"。</> },
    { key: 'payload', field: 'payload', type: 'object', value: <>内容随 type 变化。</> },
  ]

  const messageColumns: TableProps<MessageRow>['columns'] = [
    { title: '方向', dataIndex: 'direction', key: 'direction' },
    {
      title: 'type',
      dataIndex: 'type',
      key: 'type',
      render: (type: string) => <Typography.Text code>{type}</Typography.Text>,
    },
    {
      title: 'payload',
      dataIndex: 'payload',
      key: 'payload',
      render: (payload: React.ReactNode) => <Typography.Text code>{payload}</Typography.Text>,
    },
  ]

  const messages: MessageRow[] = [
    {
      key: FRAME_IMAGE_PREVIEW_TYPE,
      direction: '文档页 → 外壳',
      type: FRAME_IMAGE_PREVIEW_TYPE,
      payload: '{images, index}',
    },
    { key: 'reverse', direction: '外壳 → 文档页', type: '未定义', payload: '—' },
  ]

  const payloadFields: FieldRow[] = [
    { key: 'images', field: 'images', type: 'object[]', value: <>本页正文中的全部图片，按文档顺序。</> },
    { key: 'src', field: 'images[].src', type: 'string', value: <>图片的绝对地址。</> },
    { key: 'alt', field: 'images[].alt', type: 'string', value: <>图片的替代文本。</> },
    { key: 'index', field: 'index', type: 'number', value: <>被点击图片在 images 中的位置。</> },
  ]

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card title="概述">
        <Typography.Paragraph>文档页与外壳之间有一条通道。两者用它交换消息。</Typography.Paragraph>
        <Typography.Paragraph>文档页发消息。外壳收消息。</Typography.Paragraph>
        <Typography.Paragraph>外壳指分享页与预览页。</Typography.Paragraph>
        <Typography.Paragraph>
          本通道用于图片预览。用户在文档页点击正文中的图片，外壳显示大图。
        </Typography.Paragraph>
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          正文不需要改动。写 <Typography.Text code>![甲图](a.png)</Typography.Text> 即可。
        </Typography.Paragraph>
      </Card>

      <Card title="消息格式">
        <Typography.Paragraph>消息是一个对象，包含四个字段。</Typography.Paragraph>
        <Table<FieldRow>
          rowKey="key"
          columns={fieldColumns}
          dataSource={fields}
          pagination={false}
          scroll={{ x: 'max-content' }}
        />
      </Card>

      <Card title="消息">
        <Typography.Paragraph>版本 {FRAME_CHANNEL_VERSION} 定义一种消息。</Typography.Paragraph>
        <Table<MessageRow>
          rowKey="key"
          columns={messageColumns}
          dataSource={messages}
          pagination={false}
          scroll={{ x: 'max-content' }}
        />
        <Typography.Paragraph style={{ marginTop: 16 }}>
          <Typography.Text code>{FRAME_IMAGE_PREVIEW_TYPE}</Typography.Text> 的 payload 字段：
        </Typography.Paragraph>
        <Table<FieldRow>
          rowKey="key"
          columns={fieldColumns}
          dataSource={payloadFields}
          pagination={false}
          scroll={{ x: 'max-content' }}
        />
        <Typography.Paragraph style={{ marginTop: 16 }}>示例：</Typography.Paragraph>
        <CommandBlock>{sampleMessage}</CommandBlock>
      </Card>

      <Card title="处理规则">
        <ol>
          <li>外壳只处理它嵌入的那个页面发来的消息。其他来源的消息，外壳丢弃。</li>
          <li>外壳只显示 payload 中的图片地址。外壳不运行 payload 中的内容。</li>
          <li>外壳限制图片数量与地址长度。超限时，外壳丢弃整条消息。</li>
          <li>收发双方丢弃未知的 type。</li>
          <li>收发双方丢弃通道名或版本不匹配的消息。</li>
        </ol>
      </Card>

      <Card title="适用范围">
        <ul>
          <li>本通道只用于 docs 槽的页面。</li>
          <li>分享页与预览页提供外壳。</li>
          <li>直接打开发布域地址时没有外壳。此时点击图片不显示大图。</li>
          <li>site 槽的页面没有本通道。</li>
          <li>消息只包含数据，不包含代码。</li>
          <li>文档页读不到外壳的登录状态。外壳读不到文档页的内容与位置。</li>
        </ul>
      </Card>
    </Space>
  )
}
