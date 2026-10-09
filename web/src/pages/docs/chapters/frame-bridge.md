文档页与外壳之间有一条通道。两者用它交换消息。

文档页发消息。外壳收消息。

外壳指分享页与预览页。

本通道用于图片预览。用户在文档页点击正文中的图片，外壳显示大图。

正文不需要改动。写 `![甲图](a.png)` 即可。

## 消息格式

消息是一个对象，包含四个字段。

| 字段 | 类型 | 取值 |
|------|------|------|
| `channel` | string | 固定为 {{frame.channel}} |
| `version` | number | 固定为 {{frame.version}} |
| `type` | string | 消息类型。取值见「消息」。 |
| `payload` | object | 内容随 type 变化。 |

## 消息

版本 {{frame.version}} 定义一种消息。

| 方向 | type | payload |
|------|------|---------|
| 文档页 → 外壳 | `{{frame.image-preview}}` | `{images, index}` |
| 外壳 → 文档页 | 未定义 | — |

`{{frame.image-preview}}` 的 payload 字段：

| 字段 | 类型 | 取值 |
|------|------|------|
| `images` | object[] | 本页正文中的全部图片，按文档顺序。 |
| `images[].src` | string | 图片的绝对地址。 |
| `images[].alt` | string | 图片的替代文本。 |
| `index` | number | 被点击图片在 images 中的位置。 |

示例：

```json
{
  "channel": "{{frame.channel}}",
  "version": {{frame.version}},
  "type": "{{frame.image-preview}}",
  "payload": {
    "images": [{ "src": "<图片的绝对地址>", "alt": "甲图" }],
    "index": 0
  }
}
```

## 处理规则

1. 外壳只处理它嵌入的那个页面发来的消息。其他来源的消息，外壳丢弃。
2. 外壳只显示 payload 中的图片地址。外壳不运行 payload 中的内容。
3. 外壳限制图片数量与地址长度。超限时，外壳丢弃整条消息。
4. 收发双方丢弃未知的 type。
5. 收发双方丢弃通道名或版本不匹配的消息。

## 适用范围

- 本通道只用于 docs 槽的页面。
- 分享页与预览页提供外壳。
- 直接打开发布域地址时没有外壳。此时点击图片不显示大图。
- site 槽的页面没有本通道。
- 消息只包含数据，不包含代码。
- 文档页读不到外壳的登录状态。外壳读不到文档页的内容与位置。
