import { createClient } from '@connectrpc/connect'
import type { Client, Interceptor } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-node'

import { GalaxyService } from '../gen/proto/aladdin/galaxy/v1/galaxy_pb.js'

/**
 * 调用面与服务端之间的那一条通道：**凭证注入只有这一处**。
 *
 * 传输协议是 Connect（服务端用 connect-go，同一个端口同时支持 Connect / gRPC /
 * gRPC-Web）。这里走的是与浏览器、命令行**同一条业务实现**，因此判定不会出现
 * "浏览器生效、SDK 没生效"（见 AGENTS.md 的"传输方式的既定选择"）。
 *
 * **凭证由调用方显式给出。** 这个包不读命令行的配置文件：那会引入第二处
 * "凭证在哪"的定义，而它与命令行那份一旦漂移，表现是"命令行能用的凭证 SDK
 * 说没登录"。需要复用命令行那份凭证时，调用方自己读出来传进来即可。
 */

/** 连一个部署需要的东西。 */
export interface GalaxyConnectionOptions {
  /** 服务端地址，形如 `https://aladdin.example.com` 或 `http://127.0.0.1:8080`。 */
  address: string
  /** 访问凭证。空串表示匿名调用——匿名只能打到公开方法上。 */
  token?: string
  /** HTTP 版本。默认按地址的协议选：https 走 2，http 走 1.1。 */
  httpVersion?: '1.1' | '2'
}

/** 建一个 GalaxyService 客户端。 */
export function createGalaxyClient(options: GalaxyConnectionOptions): Client<typeof GalaxyService> {
  return createClient(GalaxyService, createGalaxyTransport(options))
}

/** 建一条传输。分开导出，是为了让调用方自己装拦截器时不必再拼一遍凭证那一层。 */
export function createGalaxyTransport(options: GalaxyConnectionOptions) {
  return createConnectTransport({
    baseUrl: options.address.replace(/\/+$/, ''),
    httpVersion: options.httpVersion ?? (options.address.startsWith('https:') ? '2' : '1.1'),
    interceptors: [bearerToken(options.token ?? '')],
  })
}

/**
 * 把凭证放进 `Authorization` 头。
 *
 * 空凭证**不设这个头**，而不是设一个空值：服务端按"有没有带凭证"区分匿名与
 * 未认证，一个空的 Bearer 会把它推到一个本该是"匿名"的分支上。
 */
function bearerToken(token: string): Interceptor {
  return (next) => async (req) => {
    if (token !== '') {
      req.header.set('Authorization', `Bearer ${token}`)
    }
    return next(req)
  }
}
