import { Card, Space, Tag, Typography } from 'antd'
import { FolderTree, Image, KeyRound, ListOrdered, ShieldAlert, Sparkles } from 'lucide-react'

import { CommandBlock } from './CommandBlock'

/**
 * 创作与发布（galaxy）介绍页。
 *
 * 它是 docs/design/galaxy/ 各子文档在界面上那一面；其中"内容是一组文件"一节对应
 * site-model.md，"素材怎么引用"一节对应 authoring.md 的记号与 asset-library.md
 * 的短时地址。
 *
 * 三条约束，改这个文件时要一起守（与 CliPage 同源）：
 *
 * - **不出现部署实例的值。** 发布地址与发布根由服务端给出，这里只写"服务端打印的
 *   地址"，把域名写进仓库就是实例值（见 docs/deploy.md 的可验证性表）。
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
          放下一组文件——手写的一页、构建工具产出的整站、或者一组 markdown 文档——再放素材，
          然后发布成一个别人打开网址就能看的站点。
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary">
          内容不是一个文件，而是<strong>一组具名文件</strong>：每个路径都有自己的地址
          （<Typography.Text code>{'/g/<工程标识>/<路径>'}</Typography.Text>
          ）。因此构建产物可以一个字节都不改地发布，文档也能一章一个网址、深链直接分享。
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          每一步各一条命令，没有「一条命令走完全流程」的形态——一次发布发的是
          <strong>某一版</strong>，而不是「文件现在的样子」。而且<strong>网页端只读</strong>
          ：内容的写入只有命令行一条路，{' '}
          <Typography.Text code>push</Typography.Text> 送上去的那一份就是全部。
        </Typography.Paragraph>
      </Card>

      <Card
        title={
          <Space size={8}>
            <FolderTree size={16} />
            选内容槽：站点、文档，或两个都要
          </Space>
        }
      >
        <Typography.Paragraph>
          建工程时<strong>至少选一个内容槽</strong>，此后<strong>只增不删</strong>——一个已经
          分享了站点的工程，可以再长出文档来，而不必另建一个工程。两个槽各有自己的草稿、版本、
          分享地址与发布状态，<strong>互不影响</strong>：推文档不会覆盖站点，撤回一个也不动
          另一个。
        </Typography.Paragraph>
        <ul>
          <li>
            <Tag>site</Tag> 整站文件<strong>原样服务</strong>。覆盖手写单页与构建产物，
            入口是 <Typography.Text code>index.html</Typography.Text>，地址是工程根
            <Typography.Text code>{'/g/<工程标识>/'}</Typography.Text>。
          </li>
          <li>
            <Tag>docs</Tag> 一组 markdown <strong>渲染成多页</strong>，站点文件（CSS、JS、JSON）
            按原路径一并带上，入口是 <Typography.Text code>index.md</Typography.Text>，
            地址是它下面的 <Typography.Text code>docs/</Typography.Text>。
          </li>
        </ul>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          <Typography.Text code>docs</Typography.Text> 槽<strong>不收 HTML</strong>
          ：它的页面是渲染出来的，不是写出来的。两者的白名单因此不同，命令行会按槽告诉你
          哪些文件能进。也正因为 <Typography.Text code>docs</Typography.Text> 这一段地址
          归文档槽，站点槽里不能有以它开头的路径。
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
          图片、视频、音频、字体先用{' '}
          <Typography.Text code>aladdin galaxy asset upload &lt;工程标识&gt; &lt;文件&gt;</Typography.Text>{' '}
          传进工程的资产库，手写内容里用记号引用它：
        </Typography.Paragraph>
        <CommandBlock>{'<img src="asset://<资产标识>">'}</CommandBlock>
        <Typography.Paragraph>
          正文里<strong>永远只有这个记号，没有真实地址</strong>
          。真实地址由系统在渲染时补上——记号写在元素属性里、写在内联 CSS 的{' '}
          <Typography.Text code>url()</Typography.Text> 里、写在{' '}
          <Typography.Text code>srcset</Typography.Text> 里，都只是一段逐字替换的文本，
          不必照着属性列表挨个写对。
        </Typography.Paragraph>
        <Typography.Paragraph>
          补出来的地址在两处不同，而<strong>两处都不进你的源文件</strong>：
        </Typography.Paragraph>
        <ul>
          <li>
            <Typography.Text strong>网页端预览</Typography.Text>
            ：短时有效的预签名地址，几分钟后过期，刷新预览即得到新的。
          </li>
          <li>
            <Typography.Text strong>发布</Typography.Text>：站点内的路径；访问它时媒体会跳到
            内容寻址的公开地址，CDN 因此缓存得住，而撤回发布仍然立刻生效。
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
          合法的地方。<strong>构建产物不需要记号</strong>——它写的就是路径，原样生效。
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
            ）成功时也会打印新资产的标识——把它写进{' '}
            <Typography.Text code>asset://</Typography.Text> 记号里。
          </Typography.Paragraph>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            那个读取地址是一份<strong>短期凭证</strong>：有效期内谁拿到都能打开，因此
            <strong>未发布的素材里不要放秘密</strong>。过期后重新执行本命令即得到新地址；内容里的记号不受影响，
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
aladdin galaxy project create --slot site --name "我的站点"

# 把发布根交给构建命令，产物里的绝对路径才对得上
vite build --base "$(aladdin galaxy project base <工程标识> --slot site)"

# 整组推送一个目录（目录里没有的路径 = 删掉），再校验
aladdin galaxy draft push <工程标识> ./dist --slot site
aladdin galaxy validate <工程标识> --slot site

# 存成一个版本，发布它，拿到地址
aladdin galaxy version save <工程标识> --slot site
aladdin galaxy publish <工程标识> <版本标识> --slot site --yes

# 后来想加上文档：加一个文档槽，再往那个槽推（槽只增不删）
aladdin galaxy project slot add <工程标识> --slot docs
aladdin galaxy draft push <工程标识> ./docs --slot docs`}</CommandBlock>
          <Typography.Paragraph type="secondary">
            <Typography.Text code>--slot</Typography.Text> 在只有一个槽的工程上可以省略；
            两个槽都有时必须给，否则命令行不知道该打到哪一个。服务端始终要求显式的槽。
          </Typography.Paragraph>
          <Typography.Paragraph type="secondary">
            <Typography.Text code>validate</Typography.Text> 校的是<strong>已保存的草稿</strong>
            （推送是唯一的写入路径），有问题时逐条列出文件与行号并以非零状态退出，所以{' '}
            <Typography.Text code>validate && publish</Typography.Text>{' '}
            这类写法成立——「这份内容不能发布」是一个结论，不是一次调用失败。
          </Typography.Paragraph>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            想把某一版取回本地用 <Typography.Text code>version pull</Typography.Text>，
            正在编辑的那一份用 <Typography.Text code>draft pull</Typography.Text>
            ：两者都是"清单 + 一串直连下载"，往返之后目录内容逐字相同。
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
          每一处<strong>取资源</strong>的引用都必须落在这一组文件里：指向外部地址、别的工程、
          或不存在的位置，发布会被拒绝，提示里带着文件和行号。链接到外部网站不受此限——那是你的意图，
          不是资源引用。<Typography.Text code>docs</Typography.Text> 槽下文档之间的相对链接同理，
          指向不存在的位置即拒绝。
        </Typography.Paragraph>
        <Typography.Paragraph>
          发布出去的站点是<strong>公开匿名</strong>的：拿到地址的人就能看，所以发布物里不要放秘密。
          正因如此，工程标识不可猜，也没有"列出所有已发布工程"的入口。
        </Typography.Paragraph>
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          <Typography.Text code>publish</Typography.Text> 与{' '}
          <Typography.Text code>project</Typography.Text> /{' '}
          <Typography.Text code>version</Typography.Text> /{' '}
          <Typography.Text code>asset</Typography.Text>{' '}
          的删除是危险操作：交互式下二次确认，脚本里必须显式传入{' '}
          <Typography.Text code>--yes</Typography.Text>
          。撤回发布（<Typography.Text code>unpublish</Typography.Text>
          ）不是——它是发布的反向操作，一步就能让每一条地址都不可达，且不产生新的暴露。
        </Typography.Paragraph>
      </Card>
    </Space>
  )
}
