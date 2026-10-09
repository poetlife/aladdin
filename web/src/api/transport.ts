import { Code, ConnectError, createClient } from '@connectrpc/connect'
import type { Interceptor, Transport } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'

import { EventsService } from '../gen/proto/aladdin/events/v1/events_pb'
import { GalaxyService } from '../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { IdentityService } from '../gen/proto/aladdin/identity/v1/identity_pb'
import { OpsService } from '../gen/proto/aladdin/ops/v1/ops_pb'
import { ProfileService } from '../gen/proto/aladdin/profile/v1/profile_pb'
import { RBACService } from '../gen/proto/aladdin/rbac/v1/rbac_pb'
import { SkillAdminService, SkillService } from '../gen/proto/aladdin/skill/v1/skill_pb'
import { TelemetryAdminService } from '../gen/proto/aladdin/telemetry/v1/telemetry_admin_pb'
import { TelemetryService } from '../gen/proto/aladdin/telemetry/v1/telemetry_pb'

/**
 * 本文件是前端所有出站请求的唯一出口（见 docs/ssot-registry.md）。
 *
 * 凭证注入、链路标识注入、会话失效处理只在这里实现一次；api/ 下的各服务模块
 * 只负责把一个 service descriptor 绑到这个 transport 上。
 * 组件不得直接 fetch，也不得自行 `createConnectTransport`。
 *
 * 链路标识用 W3C Trace Context 的 `traceparent`（见 ./trace-context）。
 * 服务端为每个请求起 span，并把同一个 trace-id、服务端自己的 span-id
 * 回写在响应头里；失败时可用 `traceIdOf(error)` 取出，见 ./errors。
 *
 * 传输协议是 Connect（参考 usememos/memos）：服务端用 connect-go，
 * 同一个端口同时支持 Connect / gRPC / gRPC-Web，因此浏览器与命令行
 * 都走 Connect，两侧共享同一份业务实现。
 */

/** 传输层配置。 */
export interface TransportOptions {
  /** 服务端地址。留空表示同源。 */
  baseUrl: string
  /** 取回当前凭证。返回 null 表示匿名调用。 */
  getToken: () => string | null
}

/** 链路标识的常量与生成/校验都在 trace-context 里，此处只做注入。 */
import { CLIENT_HEADER, CLIENT_WEB } from './client-id'
import { newTraceparent, TRACEPARENT_HEADER } from './trace-context'

/**
 * 认证拦截器。
 *
 * 它同时负责凭证注入、链路标识注入、**上报端标识注入**，以及未认证时的统一
 * 会话失效处理。
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

    // 上报端标识：服务端请求留痕据此区分浏览器与命令行。写死在这里，与
    // 客户端事件的 Client.WEB 同源（见 ./client-id）。
    req.header.set(CLIENT_HEADER, CLIENT_WEB)

    // 拿不到密码学随机数时不发这个头：缺头只是让服务端自己起一条链路，
    // 而发一个格式非法的值会污染服务端日志。
    const traceparent = newTraceparent()
    if (traceparent !== null) {
      req.header.set(TRACEPARENT_HEADER, traceparent)
    }

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

/** 个人档案服务的客户端。 */
export function profileClient() {
  return createClient(ProfileService, getTransport())
}

/** galaxy 创作服务的客户端。 */
export function galaxyClient() {
  return createClient(GalaxyService, getTransport())
}

/**
 * 事件通道的客户端。
 *
 * 它是唯一一条**流式**调用：一条连接承载多个主题（见 docs/design/events/README.md）。
 * 凭证与链路标识由同一个拦截器注入，因此这里不必也不得另写一份。
 */
export function eventsClient() {
  return createClient(EventsService, getTransport())
}

/**
 * 遥测上报服务的客户端。
 *
 * 它只被 web/src/telemetry 使用——组件不得直接调用：攒批、即时发送与失败降级都在
 * 那里统一处理（见 docs/observability.md 的「客户端事件」）。
 */
export function telemetryClient() {
  return createClient(TelemetryService, getTransport())
}

/**
 * 技能目录**读面**的客户端。
 *
 * 与下面那个维护面客户端是两个描述符：读要 skill.catalog.read，写要
 * skill.catalog.write（见 docs/design/skill/README.md）。
 */
export function skillClient() {
  return createClient(SkillService, getTransport())
}

/** 技能目录**维护面**的客户端。 */
export function skillAdminClient() {
  return createClient(SkillAdminService, getTransport())
}

/**
 * 客户端事件**只读管理面**的客户端。
 *
 * 与上面的上报客户端是同一个领域的两侧：那个是公开的写侧（任何会话都能报），
 * 这个是受控的读侧（要 telemetry.read）。因此两个描述符、两个客户端，不合并。
 */
export function telemetryAdminClient() {
  return createClient(TelemetryAdminService, getTransport())
}

/**
 * 部署实例自述的客户端。
 *
 * 它与遥测管理面同属运维分组，但两者没有共同状态：那个查的是落库的事件，这个读的
 * 是构建期注入加运行期常量。因此是另一个服务描述符、另一道权限门
 * （ops.deployment.read），不合并。
 */
export function opsClient() {
  return createClient(OpsService, getTransport())
}
