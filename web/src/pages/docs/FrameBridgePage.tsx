import { Card, Space, Tag, Typography } from 'antd'
import { FolderTree, Image, ListOrdered, ShieldAlert, Sparkles } from 'lucide-react'

import { FRAME_CHANNEL, FRAME_CHANNEL_VERSION, FRAME_IMAGE_PREVIEW_TYPE } from '../galaxy/frame-channel'
import { CommandBlock } from './CommandBlock'

/**
 * 页面与外壳的通道（把 spec 里那份协议讲给**作者**听）。
 *
 * 它是 docs/design/galaxy/site-model.md 的"平台接入桥"在界面上那一面，壳那一侧见
 * publication.md；章节归属见 docs/design/web/docs-area.md。
 *
 * 与其余章节有一处根本不同，改这个文件时要清楚：**它写的是"页面长在什么上"，不是
 * "你要做什么"。** 页里的图片能点开，作者一行都不用写。这一章存在的理由是那条通道
 * 是一份**对外契约**——通道名、协议版本、消息类型、两侧各判什么，都是作者可以依赖
 * 的东西。
 *
 * 三条约束（与 GalaxyPage 同源）：
 *
 * - **不出现部署实例的值。** 示例里的地址一律写 `…`。
 * - **正文里不出现"只有登录者才有"的东西**：本区将来要放出去（见 docs-area.md），
 *   依赖读者的内容一旦进去，那次放出去就会变成一次内容重写。
 * - **协议取值从实现里取**（`frame-channel.ts` 导出什么就写什么），不手抄一份——
 *   手抄的那份会在协议改动那天不声不响地说错话。
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

  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <Image size={16} />
            点开一张图，是平台做的事
          </Space>
        }
      >
        <Typography.Paragraph>
          文档（<Typography.Text code>docs</Typography.Text>）槽的页面里，点正文中的一张图会打开
          <strong>满屏预览</strong>：遮罩铺满整个视口，大图居中，多张图用左右方向键切换，Esc 关闭。
          分享页与预览页都有，<strong>你不需要写任何东西</strong>——正文里写{' '}
          <Typography.Text code>![甲图](a.png)</Typography.Text> 就够了。
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          为什么不是页面自己弹一层遮罩：页面被关在一个跨源沙箱里，它画的遮罩只能盖住内容那一块，
          出了内容区就点不到——那不是一次满屏预览。所以大图由<strong>外面那一层</strong>来画，
          下面简称<strong>外壳</strong>（就是分享页与预览页）。下面这条通道，就是页面把
          「用户点了哪张图」告诉外壳用的。
        </Typography.Paragraph>
      </Card>

      <Card
        title={
          <Space size={8}>
            <ListOrdered size={16} />
            通道：一只有版本的信封
          </Space>
        }
      >
        <Typography.Paragraph>
          页面与外壳互发的是同一种信封，<strong>两侧都只认下面写着的东西，其余一律忽略</strong>。
          因此加一种消息是增量的：信封不变，多一个类型。
        </Typography.Paragraph>
        <ul>
          <li>
            <Tag>channel</Tag>
            <Typography.Text code>{FRAME_CHANNEL}</Typography.Text>
            ：通道标识，两侧都只认这一个值。
          </li>
          <li>
            <Tag>version</Tag>
            <Typography.Text code>{FRAME_CHANNEL_VERSION}</Typography.Text>
            ：协议版本。收方不认识的版本一律忽略——因此新版外壳与旧版页面相遇时，双方都安分。
          </li>
          <li>
            <Tag>type</Tag>：消息类型，见下表。
          </li>
          <li>
            <Tag>payload</Tag>：随类型而定。
          </li>
        </ul>
        <Typography.Paragraph>
          <Typography.Text strong>目前只有一种消息</Typography.Text>，方向和载荷如下：
        </Typography.Paragraph>
        <ul>
          <li>
            页面 → 外壳：<Typography.Text code>{FRAME_IMAGE_PREVIEW_TYPE}</Typography.Text>
            ，载荷 <Typography.Text code>{'{images: [{src, alt}], index}'}</Typography.Text>
            。用户点了正文里的一张图时发出，<Typography.Text code>images</Typography.Text>{' '}
            是<strong>这一页正文里的全部图</strong>（按文档顺序）——每张带浏览器解析出来的
            <strong>绝对地址</strong>与它的替代文本——<Typography.Text code>index</Typography.Text>{' '}
            是被点的那一张的位置。
          </li>
          <li>
            外壳 → 页面：<Typography.Text strong>这一版没有定义任何类型</Typography.Text>。方向是通的，
            加一种外壳侧的消息不必再改协议。
          </li>
        </ul>
        <CommandBlock>{sampleMessage}</CommandBlock>
      </Card>

      <Card
        title={
          <Space size={8}>
            <ShieldAlert size={16} />
            两侧各判什么
          </Space>
        }
      >
        <Typography.Paragraph>
          <Typography.Text strong>外壳只认自己那一帧。</Typography.Text>
          它不按"消息从哪个来源地址来"判——页面落在沙箱里、来源标识是字符串{' '}
          <Typography.Text code>null</Typography.Text>，而任何一个沙箱帧都是{' '}
          <Typography.Text code>null</Typography.Text>，那个值认不出是谁发的。所以判据是帧本身：
          别的页面发来的同款消息，外壳直接丢掉。因此你也不必费心"伪造一条消息"——来源不对就进不去。
        </Typography.Paragraph>
        <Typography.Paragraph>
          <Typography.Text strong>外壳只渲染，不执行。</Typography.Text>
          载荷里的图片地址只会被放进一个 <Typography.Text code>&lt;img&gt;</Typography.Text>
          ，不会被当成页面标记、更不会被当成代码。所以别把"请执行某件事"塞进载荷——它不会被执行。
        </Typography.Paragraph>
        <Typography.Paragraph>
          <Typography.Text strong>载荷能有多大，不由发消息那一侧说了算。</Typography.Text>
          外壳对图片张数与每张地址的长度都有上限，越界的整条丢掉：一条消息能决定外壳页面上挂多少张图，
          这个决定权不该只在页面手里。
        </Typography.Paragraph>
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          <Typography.Text strong>页面只认信封。</Typography.Text>
          通道或版本不认识就丢，认不出的类型一律忽略——因此外壳先升级、页面后升级，中间不会有半截行为。
        </Typography.Paragraph>
      </Card>

      <Card
        title={
          <Space size={8}>
            <FolderTree size={16} />
            这条通道不管什么
          </Space>
        }
      >
        <Typography.Paragraph>
          <Typography.Text strong>只有文档槽的页面有它。</Typography.Text>
          <Tag>site</Tag>
          槽交付的是<strong>你自己的整站</strong>，aladdin 一个字节都不加——那里没有这条通道，
          页里的图片点开是什么样，完全由你自己决定。
        </Typography.Paragraph>
        <Typography.Paragraph>
          <Typography.Text strong>没有外壳就没有大图。</Typography.Text>
          <strong>直接打开</strong>发布域上那条地址（不经过分享页）时，页面照常显示，点图不弹——
          那条路上没有一个"外面那一层"。分享页与预览页是仅有的两个宿主。
        </Typography.Paragraph>
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          <Typography.Text strong>它不改变隔离，也不给外壳多知道什么。</Typography.Text>
          通道只传数据，不传代码、不传句柄：页面仍然读不到外壳的登录态、本地存储与 DOM；
          外壳也仍然读不到页面的 DOM，连"你停在文档站的哪一页"都读不到——位置不在任何一条消息里。
        </Typography.Paragraph>
      </Card>

      <Card
        title={
          <Space size={8}>
            <Sparkles size={16} />
            你还能做什么
          </Space>
        }
      >
        <Typography.Paragraph>
          通道上的脚本你不必只当它是平台的：文档槽收 <Typography.Text code>.js</Typography.Text>{' '}
          站点文件，正文里也允许内联 HTML，因此页面里跑什么由你决定。但
          <strong>外壳只对上表里的类型有反应</strong>——你自己发的消息它不认识就丢掉，不会报错、
          也不会有半截行为。
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          想让外壳支持一种新消息（比如别的高级渲染），那是平台侧要加的：加的时候信封与上面那几条判据
          沿用同一份，不另立一套。协议本身只有一处定义，见{' '}
          <Typography.Text code>docs/design/galaxy/site-model.md</Typography.Text> 的"平台接入桥"。
        </Typography.Paragraph>
      </Card>
    </Space>
  )
}
