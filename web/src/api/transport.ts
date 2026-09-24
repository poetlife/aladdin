import { Code, ConnectError, createClient } from '@connectrpc/connect'
import type { Interceptor, Transport } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'

import { IdentityService } from '../gen/proto/aladdin/identity/v1/identity_pb'
import { RBACService } from '../gen/proto/aladdin/rbac/v1/rbac_pb'

/**
 * 本文件是前端所有出站请求的唯一出口（见 docs/ssot-registry.md）。
 *
 * 凭证注入、链路标识、会话失效处理只在这里实现一次；api/ 下的各服务模块
 * 只负责把一个 service descriptor 绑到这个 transport 上。
 * 组件不得直接 fetch，也不得自行 `createConnectTransport`。
 *
 * 传输协议是 Connect（参考 usememos/memos）：服务端用 connect-go，
 * 同一个端口同时支持 Connect / gRPC / gRPC-Web，因此浏览器走 Connect、
 * CLI 走原生 gRPC，两侧共享同一份业务实现。
 */

/** 传输层配置。 */
export interface TransportOptions {
  /** 服务端地址。留空表示同源。 */
  baseUrl: string
  /** 取回当前凭证。返回 null 表示匿名调用。 */
  getToken: () => string | null
}

/** 链路标识的请求头名，与服务端 observability.TraceIDHeader 一致。 */
const TRACE_ID_HEADER = 'x-trace-id'

/**
 * 生成链路标识。
 *
 * 每次调用生成一个新的，与 CLI 的行为一致；服务端把它写进所有相关日志，
 * 使一次前端操作可以在服务端日志中被完整还原。
 */
function newTraceID(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID().replace(/-/g, '')
  }
  return `trace-${Math.random().toString(16).slice(2)}`
}

/**
 * 认证拦截器。
 *
 * 它同时负责凭证注入与链路标识注入，以及未认证时的统一会话失效处理。
 *
 * 关于"刷新后重试"：服务端目前签发的凭证不过期，因此这里不假装实现重试循环。
 * 接入真实凭证后，在 catch 分支里加"刷新一次并重放请求"即可——重放时务必
 * 带一个标记头避免无限循环（参考 memo 的 X-Retry 做法）。放在这里而不是
 * 各调用点，是为了避免每个页面各写一遍重试逻辑。
 */
function createAuthInterceptor(options: TransportOptions): Interceptor {
  return (next) => async (req) => {
    const token = options.getToken()
    if (token !== null && token !== '') {
      req.header.set('Authorization', `Bearer ${token}`)
    }
    req.header.set(TRACE_ID_HEADER, newTraceID())

    try {
      return await next(req)
    } catch (error) {
      if (error instanceof ConnectError && error.code === Code.Unauthenticated) {
        notifyUnauthenticated()
      }
      throw error
    }
  }
}

/** 线格式的运行时覆盖参数名，用法：`?wire=json` / `?wire=binary`。 */
const WIRE_FORMAT_PARAM = 'wire'

/**
 * 决定浏览器端用哪种线格式。
 *
 * 开发环境默认 JSON：DevTools 的 Preview / Response 面板能直接看结构化报文，
 * 排查时不必对着 hex 逐字节转译，也能右键复制成可重放的请求。
 * 生产环境默认二进制（更省带宽），但保留运行时覆盖——线上遇到必须看响应体的
 * 问题时加一个查询参数刷新即可，不必为此走一次发版流程。
 *
 * 服务端同一端口同时接受两种编码，所以这里只决定浏览器这一侧怎么编码，
 * 不是权限边界，也无法被外部输入用来绕过任何判定。
 */
export function resolveBinaryFormat(search: string): boolean {
  const override = new URLSearchParams(search).get(WIRE_FORMAT_PARAM)
  if (override === 'json') {
    return false
  }
  if (override === 'binary') {
    return true
  }
  return import.meta.env.PROD
}

/** 构造传输层。 */
export function createTransport(options: TransportOptions): Transport {
  return createConnectTransport({
    baseUrl: options.baseUrl,
    useBinaryFormat: resolveBinaryFormat(window.location.search),
    interceptors: [createAuthInterceptor(options)],
  })
}

/** 当前生效的传输层。 */
let activeTransport: Transport | null = null

/** 设置传输层。应用启动时调用一次。 */
export function setTransport(transport: Transport): void {
  activeTransport = transport
}

let unauthHandler: () => void = () => {}

/**
 * 注册"会话失效"回调。
 *
 * 用注册式而不是把回调塞进 TransportOptions，是为了断开构造顺序上的环：
 * 传输层要在 React 渲染之前就绪，而处理会话失效的会话层是 React 组件，
 * 那时还不存在。会话层挂载后再把它自己注册进来。
 */
export function onUnauthenticated(handler: () => void): void {
  unauthHandler = handler
}

/** 触发会话失效处理。由传输层的认证拦截器调用。 */
export function notifyUnauthenticated(): void {
  unauthHandler()
}

/** 取回当前传输层，未初始化时抛出而非静默返回空实现。 */
export function getTransport(): Transport {
  if (activeTransport === null) {
    throw new Error('传输层尚未初始化：请在应用启动时调用 setTransport')
  }
  return activeTransport
}

/**
 * 各服务的客户端。
 *
 * 它们是函数而不是顶层常量：客户端要在传输层就绪之后才能构造，
 * 而传输层的初始化依赖会话层，两者存在构造顺序上的先后。
 */
/** 身份认证服务的客户端。 */
export function identityClient() {
  return createClient(IdentityService, getTransport())
}

/** 权限管理服务的客户端。 */
export function rbacClient() {
  return createClient(RBACService, getTransport())
}
