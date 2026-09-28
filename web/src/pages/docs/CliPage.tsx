import { Card, Space, Tabs, Typography, theme } from 'antd'
import { Download, KeyRound, RefreshCw, Terminal } from 'lucide-react'

import { MONOSPACE } from '../../theme'

/**
 * 命令行介绍页。
 *
 * 它是 docs/design/cli/install.md 在界面上的那一面。在这之前，"怎么装命令行"
 * 在仓库里没有落点——只有"怎么升级"，而升级的前提恰恰是先有一份**发布产物**
 * （本机编译出来的二进制不能自更新）。
 *
 * 三条硬约束，改这个文件时要一起守：
 *
 * - **不出现版本号字面量。** 产物名里带着 tag，写死一条下载命令就等于写死了
 *   当时的版本；下一个 tag 之后那条命令指向一个不存在的产物，而这里不会报
 *   任何错。所以命令在**运行期**解析最新 tag。
 * - **不出现部署实例的值**（域名、主机别名）。与仓库"不含实例值"是同一条
 *   约束，见 docs/deploy.md 的可验证性表。
 * - **不把人引向系统目录。** 自更新替换的是本机这一份文件，目标不可写时它
 *   会拒绝（见 docs/design/cli/self-update.md）。装进 /usr/local/bin 的人
 *   每次升级都要 sudo，非特权用户更是连装都装不上。
 */

/** 发布页。产物命名与保留策略见 docs/release.md。 */
const RELEASES_URL = 'https://github.com/poetlife/aladdin/releases'

/**
 * 解析最新 tag 的两行命令。
 *
 * `/releases/latest` 是一次重定向（落到 `/releases/tag/<tag>`），
 * `url_effective` 给出落地地址。**不自建"最新版本"的解析**：那是发布侧的事实，
 * 第二个实现只会在某天与它对不上。
 */
const RESOLVE_LATEST = `tag=$(curl -fsSL -o /dev/null -w '%{url_effective}' \\
  ${RELEASES_URL}/latest)
tag=\${tag##*/}`

interface Platform {
  /** 产物名里的平台段，形如 `darwin_arm64`。 */
  readonly key: string
  readonly label: string
  /** 校验和工具的名字：coreutils 与 macOS 自带的那份不同。 */
  readonly verify: string
}

/**
 * 支持哪些平台**不自建**：它取自 Makefile 的 PLATFORMS，契约在 docs/release.md。
 * 这里是消费方，不是第二份定义。
 */
const PLATFORMS: readonly Platform[] = [
  { key: 'darwin_arm64', label: 'macOS（Apple Silicon）', verify: 'shasum -a 256 -c -' },
  { key: 'linux_amd64', label: 'Linux（x86-64）', verify: 'sha256sum -c -' },
]

/** 一个平台的完整安装命令。tag 全程是运行期解析出来的，没有一个字面量。 */
function installScript(platform: Platform): string {
  return `${RESOLVE_LATEST}

# 产物地址与产物名各拼一次，下面只用这两个变量
base="${RELEASES_URL}/download/$tag"
asset="aladdin_\${tag}_${platform.key}.tar.gz"

curl -LO "$base/$asset"
curl -LO "$base/SHA256SUMS"
grep "$asset" SHA256SUMS | ${platform.verify}
tar xzf "$asset"

# 装进用户可写目录，这样升级不需要 sudo
mkdir -p ~/.local/bin
install -m 0755 aladdin ~/.local/bin/aladdin`
}

/**
 * 命令行介绍页：把 docs/design/cli/install.md 的结论搬到界面上——
 * 装什么、怎么登录、怎么升级。
 */
export function CliPage(): React.ReactNode {
  return (
    <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
      <Card
        title={
          <Space size={8}>
            <Terminal size={16} />
            命令行
          </Space>
        }
      >
        <Typography.Paragraph>
          aladdin 的命令行客户端。装在你自己机器上，用已有的账号登录，做权限与角色的日常操作。
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          判定发生在服务端：本工具不缓存、也不复用任何判定结果，因此服务端的角色改动无需在客户端做任何事。
          有哪些子命令见 <Typography.Text code>aladdin --help</Typography.Text>。
        </Typography.Paragraph>
      </Card>

      <Card
        title={
          <Space size={8}>
            <Download size={16} />
            安装
          </Space>
        }
      >
        <Typography.Paragraph>
          只有<strong>发布产物</strong>可以安装：升级只认发布产物，本机构建的二进制（
          <Typography.Text code>make build</Typography.Text>、
          <Typography.Text code>go install</Typography.Text>）日后无法自更新。
        </Typography.Paragraph>
        <Tabs
          items={PLATFORMS.map((platform) => ({
            key: platform.key,
            label: platform.label,
            children: (
              <Space orientation="vertical" size="small" style={{ width: '100%' }}>
                <CommandBlock>{installScript(platform)}</CommandBlock>
                <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
                  装进 <Typography.Text code>~/.local/bin</Typography.Text>
                  ——它归你所有，所以后面的{' '}
                  <Typography.Text code>aladdin update</Typography.Text> 不需要 sudo；装到{' '}
                  <Typography.Text code>/usr/local/bin</Typography.Text>{' '}
                  这类系统目录则要先过 sudo，普通用户还会直接 Permission denied。这个目录不在{' '}
                  <Typography.Text code>PATH</Typography.Text> 里时，把{' '}
                  <Typography.Text code>{'export PATH="$HOME/.local/bin:$PATH"'}</Typography.Text>{' '}
                  加进 shell 配置并重开一个终端。装完用{' '}
                  <Typography.Text code>aladdin version</Typography.Text> 确认。
                </Typography.Paragraph>
              </Space>
            ),
          }))}
        />
      </Card>

      <Card
        title={
          <Space size={8}>
            <KeyRound size={16} />
            登录
          </Space>
        }
      >
        <Space orientation="vertical" size="small" style={{ width: '100%' }}>
          <CommandBlock>{'aladdin login'}</CommandBlock>
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            终端打印一个短码，在一个<strong>已经登录</strong>的浏览器里打开服务端给出的批准页，输入短码即完成。
            短码要由人比对，所以它不进地址、也不进访问日志。
          </Typography.Paragraph>
          <CommandBlock>{'aladdin login --token <凭证>'}</CommandBlock>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            机器凭证那条路径：适合脚本与 CI，不走浏览器。凭证按「参数 &gt; 环境变量 &gt; 凭证文件」解析，
            保存在本机。
          </Typography.Paragraph>
        </Space>
      </Card>

      <Card
        title={
          <Space size={8}>
            <RefreshCw size={16} />
            升级
          </Space>
        }
      >
        <Space orientation="vertical" size="small" style={{ width: '100%' }}>
          <CommandBlock>{'aladdin update --check   # 只报告有没有新版本\naladdin update           # 升级到最新发布版本'}</CommandBlock>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            升级换的是本机那一个文件，<strong>当前这个进程仍是旧版本</strong>——运行的影像是已经打开的那个文件。
            本机构建出来的二进制会被拒绝，理由它会说清楚。
          </Typography.Paragraph>
        </Space>
      </Card>

      <Card title="服务端地址">
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          由 <Typography.Text code>--address</Typography.Text>、
          <Typography.Text code>ALADDIN_ADDRESS</Typography.Text> 或配置文件给出，默认{' '}
          <Typography.Text code>127.0.0.1:9090</Typography.Text>。把一个远端服务端接到本机属于部署形态，
          不在这里展开。
        </Typography.Paragraph>
      </Card>
    </Space>
  )
}

/**
 * 等宽命令块。它只做"命令要能整段复制"这一件事，所以留在本文件里；出现第二个
 * 使用方时再抽出去（过早抽出去只会多一个只有一处调用的模块）。
 *
 * 复制入口用 antd 的 `Typography` 自带的那一个（仓库里已有九处同样的用法），
 * 不自己碰剪贴板：手动选中一段命令容易漏掉续行，也容易把行首的换行带进去。
 */
function CommandBlock({ children }: { children: string }): React.ReactNode {
  const { token } = theme.useToken()

  return (
    <div style={{ position: 'relative' }}>
      <pre
        style={{
          margin: 0,
          padding: token.paddingSM,
          // 右上角留给复制入口：正文不钻到它下面去。
          paddingRight: token.paddingSM + token.padding,
          background: token.colorFillTertiary,
          borderRadius: token.borderRadius,
          fontFamily: MONOSPACE,
          fontSize: 13,
          // 命令普遍很长，窄屏下尤其。**折行而不是横向滚动**：横向滚动会把
          // 后半截藏起来，而这里藏的是 URL——看着像命令就长这样。
          whiteSpace: 'pre-wrap',
          overflowWrap: 'anywhere',
        }}
      >
        {children}
      </pre>
      {/* 没有子节点，因此渲染出来的就是这个复制图标本身。 */}
      <Typography.Text
        copyable={{ text: children }}
        style={{ position: 'absolute', top: token.paddingXS, right: token.paddingXS }}
      />
    </div>
  )
}
