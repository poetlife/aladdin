import { Card, Space, Typography } from 'antd'
import { Image, KeyRound, ListOrdered, ShieldAlert, Sparkles } from 'lucide-react'

import { CommandBlock } from './CommandBlock'

/**
 * 创作与发布（galaxy）介绍页。
 *
 * 它是 docs/design/galaxy/ 各子文档在界面上那一面；其中"素材怎么引用"一节对应
 * authoring.md 的资产占位符与 asset-library.md 的短时地址。
 *
 * 三条约束，改这个文件时要一起守（与 CliPage 同源）：
 *
 * - **不出现部署实例的值。** 发布地址由服务端给出，这里只写"服务端打印的地址"，
 *   把域名写进仓库就是实例值（见 docs/deploy.md 的可验证性表）。
 * - **示例正文里不出现真实地址。** 本节要教的恰恰是"正文里只有一个记号"，
 *   示例里放一个具体地址会把这句话说反。
 * - **命令名与位置参数与 cmd/aladdin 一致**，改动时要连同那里的 Use 一起核对。
 */
export function GalaxyPage(): React.ReactNode {
  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <Sparkles size={16} />
            创作与发布
          </Space>
        }
      >
        <Typography.Paragraph>
          写一份完整的 HTML，发布成一个别人打开网址就能看的页面。命令行与网页端能力一一对等，
          因此发布链路可以直接编进脚本与 CI。
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          你写的是一整份 HTML（<Typography.Text code>{'<!doctype html>'}</Typography.Text> 到{' '}
          <Typography.Text code>{'</html>'}</Typography.Text>
          ）：系统不替你包裹、不补全，也不注入任何东西，发布出去的就是你写的那一份。每一步各一条命令，
          没有「一条命令走完全流程」的形态——一次发布发的是<strong>某一版</strong>
          ，而不是「文件现在的样子」。
        </Typography.Paragraph>
      </Card>

      <Card
        title={
          <Space size={8}>
            <Image size={16} />
            引用素材：写记号，不写地址
          </Space>
        }
      >
        <Typography.Paragraph>
          图片、视频、音频先用{' '}
          <Typography.Text code>aladdin galaxy asset upload</Typography.Text>{' '}
          传进工程的资产库，正文里用记号引用它：
        </Typography.Paragraph>
        <CommandBlock>{'<img src="asset://<资产标识>">'}</CommandBlock>
        <Typography.Paragraph>
          正文里<strong>永远只有这个记号，没有真实地址</strong>
          。真实地址（含访问签名）由系统在渲染正文时补上——记号写在元素属性里、写在内联 CSS 的{' '}
          <Typography.Text code>url()</Typography.Text> 里、写在{' '}
          <Typography.Text code>srcset</Typography.Text> 里，都是一段逐字替换的文本，不必照着属性列表挨个写对。
        </Typography.Paragraph>
        <Typography.Paragraph>
          补地址发生在两处，两处各有一套地址：
        </Typography.Paragraph>
        <ul>
          <li>
            <Typography.Text strong>网页端预览</Typography.Text>
            ：换成短时有效的预签名地址，几分钟后过期，刷新预览即得到新的。
          </li>
          <li>
            <Typography.Text strong>发布</Typography.Text>：换成稳定的公开地址，写进发布产物。
          </li>
        </ul>
        <Typography.Paragraph type="secondary">
          为什么不直接把地址写进正文：编辑态的地址会过期，发布态的地址又不同——写死了，正文里就躺着
          一个会漂移、甚至哪天失效的字符串。而且「这个引用是不是本工程的素材」会退化成拿地址前缀去比对，
          能被相似域名、大小写、重定向绕开。记号把这两件事都变成确定的：地址由系统给；是不是本工程素材，
          就是查一次标识。
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          <Typography.Text code>asset://</Typography.Text> 不是浏览器认识的协议，这是有意的：任何一次
          漏掉补地址的场合都会<strong>显式坏掉</strong>（图片裂开、链接点不动），而不是静默指向一个看起来
          合法的地方。
        </Typography.Paragraph>
      </Card>

      <Card
        title={
          <Space size={8}>
            <KeyRound size={16} />
            看素材、拿资产标识
          </Space>
        }
      >
        <Space orientation="vertical" size="small" style={{ width: '100%' }}>
          <CommandBlock>{'aladdin galaxy asset list <工程标识>'}</CommandBlock>
          <Typography.Paragraph>
            列出资产库里的每一项：标识、类型、字节数、文件名，以及一个短时读取地址。上传（
            <Typography.Text code>asset upload</Typography.Text>
            ）成功时也会打印新资产的标识——把它写进正文的 <Typography.Text code>asset://</Typography.Text> 记号里。
          </Typography.Paragraph>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            那个读取地址是一份<strong>短期凭证</strong>：有效期内谁拿到都能打开，因此
            <strong>未发布的素材里不要放秘密</strong>。过期后重新执行本命令即得到新地址；正文里的记号不受影响，
            因为它本来就不存地址。
          </Typography.Paragraph>
        </Space>
      </Card>

      <Card
        title={
          <Space size={8}>
            <ListOrdered size={16} />
            走一遍
          </Space>
        }
      >
        <Space orientation="vertical" size="small" style={{ width: '100%' }}>
          <CommandBlock>{`# 建工程，记下它打印的工程标识
aladdin galaxy project create --name "我的页面"

# 传素材，记下它打印的资产标识，再把它写进正文的 asset:// 记号
aladdin galaxy asset upload <工程标识> ./cover.png

# 存草稿、校验、存成一个版本
aladdin galaxy draft save <工程标识> --file page.html
aladdin galaxy validate <工程标识> --file page.html
aladdin galaxy version save <工程标识>

# 发布某一版，拿到地址
aladdin galaxy publish <工程标识> <版本标识> --yes`}</CommandBlock>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            <Typography.Text code>validate</Typography.Text> 有问题时逐条列出并以非零状态退出，所以{' '}
            <Typography.Text code>validate && ...</Typography.Text>{' '}
            这类写法成立——「这段正文不能发布」是一个结论，不是一次调用失败。也可以先{' '}
            <Typography.Text code>draft get</Typography.Text> 把草稿取回本地改。
          </Typography.Paragraph>
        </Space>
      </Card>

      <Card
        title={
          <Space size={8}>
            <ShieldAlert size={16} />
            边界与危险操作
          </Space>
        }
      >
        <Typography.Paragraph>
          正文里每个记号都必须指向<strong>本工程</strong>的现存素材。指向不存在的、别的工程的、或已删除的素材，
          发布会被拒绝，提示里带着行号与那个记号，不用自己在一份几百行的正文里找。
        </Typography.Paragraph>
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          <Typography.Text code>publish</Typography.Text> 与{' '}
          <Typography.Text code>project</Typography.Text> /{' '}
          <Typography.Text code>version</Typography.Text> /{' '}
          <Typography.Text code>asset</Typography.Text>{' '}
          的删除是危险操作：交互式下二次确认，脚本里必须显式传入{' '}
          <Typography.Text code>--yes</Typography.Text>
          。撤回发布（<Typography.Text code>unpublish</Typography.Text>
          ）不是——它是发布的反向操作，一步就能让地址不可达，且不产生新的暴露。
        </Typography.Paragraph>
      </Card>
    </Space>
  )
}
