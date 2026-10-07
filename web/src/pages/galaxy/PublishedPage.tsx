import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Flex, Typography } from 'antd'
import { useLocation } from 'react-router-dom'

import * as galaxyApi from '../../api/galaxy'
import { messageOf, traceIdOf } from '../../api/errors'
import { LoadingHint } from '../../ui/LoadingHint'
import { SandboxFrame } from './SandboxFrame'

type ShellState =
  | { status: 'loading' }
  /** 解析到了内容：iframe 该指向的那一条地址。 */
  | { status: 'found'; contentUrl: string }
  /** 统一的否定结论：这一页不存在（未发布 / 已撤回 / 槽未启用 / 工程不存在 / 标识没被猜中 / 路径不在集合）。 */
  | { status: 'notfound' }
  | { status: 'failed'; message: string; traceId: string | null }

/**
 * 主站壳：一条已发布内容的**分享地址**落点。
 *
 * 它自己**不含用户内容**，只做一件事——把分享路径解析成发布域上的同一条路径，再
 * 交给沙箱 iframe（见 docs/design/galaxy/publication.md 的"主站壳"）。因此：
 *
 * - **匿名**：访客打开分享地址时没有会话，这一页不做任何准入判定，也不挂在
 *   `RequirePermission` 下。解析走的是唯一一个匿名可用的 galaxy 调用。
 * - **不列举**：它只按地址回答"内容在哪"，没有"列出全部已发布工程"的形状。
 *   "地址即凭据"因此不降级。
 * - **不裸露发布域**：页面本身不展示内容地址；iframe 的 `src` 里自然带着它，但那
 *   是浏览器的事，不是给用户看的。
 * - **不跟随 iframe 内的跳转**：跨源且不往发布物里注入脚本，壳读不到 iframe 里的
 *   位置。深链在**打开时**生效一次——分享 `/g/<标识>/docs/guide` 就落在那一页。
 */
export function PublishedPage(): React.ReactNode {
  const location = useLocation()
  const [state, setState] = useState<ShellState>({ status: 'loading' })

  // 解析的是**整条地址**而不是路由参数：槽的判定（`docs` 那一段）只有服务端一处，
  // 前端不做第二份形状解析。`location.pathname` 是百分比编码的形态，与服务端收到
  // 一条直连请求时的还原方式对应。
  const resolve = useCallback(async (pathname: string): Promise<void> => {
    setState({ status: 'loading' })
    try {
      const response = await galaxyApi.resolveSharedPage(pathname)
      if (response.contentUrl === '') {
        setState({ status: 'notfound' })
        return
      }
      setState({ status: 'found', contentUrl: response.contentUrl })
    } catch (err) {
      // 取不到解析结果与"这一页不存在"是两件事：前者可重试，后者是结论。
      setState({ status: 'failed', message: messageOf(err), traceId: traceIdOf(err) })
    }
  }, [])

  useEffect(() => {
    void resolve(location.pathname)
  }, [resolve, location.pathname])

  return (
    <Flex vertical style={{ height: '100dvh' }}>
      <ShellBody state={state} onRetry={() => void resolve(location.pathname)} />
    </Flex>
  )
}

function ShellBody({
  state,
  onRetry,
}: {
  state: ShellState
  onRetry: () => void
}): React.ReactNode {
  switch (state.status) {
    case 'loading':
      // 与内容那一帧里的加载态**同一个组件、同一句话**：对访客来说"壳还没问出地址在
      // 哪"与"内容还在路上"是同一次等待，中间换一次措辞只会让人以为已经出了别的事。
      return <LoadingHint />
    case 'found':
      // 撑满：壳除了这一框没有别的内容。
      return <SandboxFrame url={state.contentUrl} title="已发布页面" height="100%" />
    case 'notfound':
      return (
        <Centered>
          <Typography.Title level={4} style={{ marginBottom: 0 }}>
            页面不存在
          </Typography.Title>
        </Centered>
      )
    case 'failed':
      return (
        <Centered>
          <Alert
            type="error"
            showIcon
            title={state.message}
            description={
              state.traceId !== null && (
                <Typography.Text type="secondary" copyable>
                  追踪 ID：{state.traceId}
                </Typography.Text>
              )
            }
            action={<Button onClick={onRetry}>重试</Button>}
          />
        </Centered>
      )
  }
}

function Centered({ children }: { children: React.ReactNode }): React.ReactNode {
  return (
    <Flex flex={1} align="center" justify="center" style={{ padding: 24 }}>
      {children}
    </Flex>
  )
}
