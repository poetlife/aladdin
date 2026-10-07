import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'

import type { TraceCapture } from '../api/call-trace'
import * as identityApi from '../api/identity'
import { messageOf } from '../api/errors'
import { onUnauthenticated } from '../api/transport'
import type { WhoAmIResponse } from '../gen/proto/aladdin/identity/v1/identity_pb'
import { PermissionSet } from './permission-set'

/** 会话状态。 */
export type SessionStatus = 'loading' | 'anonymous' | 'authenticated' | 'error'

/** 会话上下文的值。 */
export interface SessionState {
  status: SessionStatus
  /** 当前主体。未登录时为 null。 */
  subject: WhoAmIResponse | null
  /** 当前作用域。所有权限判断都在这个作用域下进行。 */
  scope: string
  /** 已展开的权限码集合。 */
  permissions: PermissionSet
  /** 加载失败时的错误信息。 */
  error: string | null
  /**
   * 使用机器凭证或访问令牌登录。
   *
   * `trace` 可选：登录页要上报一条 `auth.login` 事件，而服务端的链路标识只在
   * 响应头里，因此**要上报的那一方**把捕获点传进来（见 api/call-trace）。不需要
   * 上报的调用方不传，行为与从前完全一致。
   */
  signIn: (token: string, trace?: TraceCapture) => Promise<void>
  /**
   * 采纳一份**已经拿到**的会话凭证。
   *
   * 重定向型登录渠道（如 GitHub、Google）由服务端完成校验，凭证随一次跳转
   * 交回前端，前端只在回调页把它取出来交给这里——它不经由任何 RPC，因此
   * 不能走 signIn。
   */
  adoptSessionToken: (accessToken: string) => Promise<void>
  /** 清除本地凭证。 */
  signOut: () => void
  /** 切换作用域并重新拉取权限码。 */
  setScope: (scope: string) => Promise<void>
  /** 重新拉取会话与权限码。 */
  refresh: () => Promise<void>
}

const SessionContext = createContext<SessionState | null>(null)

/**
 * 一次 load 的结果。
 *
 * 失败是**返回值**而不是异常：常规调用方（挂载时、切作用域）只关心状态已经落好，
 * 让 load 抛出会把它们的调用变成未处理的拒绝。只有需要知道"这次没成功"的调用方
 * ——重定向登录的回调页——自己把这个结果转成异常。
 */
type LoadOutcome = { ok: true } | { ok: false; message: string }

/** 凭证在 localStorage 中的键名。 */
const TOKEN_STORAGE_KEY = 'aladdin.token'

/** 作用域在 localStorage 中的键名。 */
const SCOPE_STORAGE_KEY = 'aladdin.scope'

function readToken(): string | null {
  return globalThis.localStorage?.getItem(TOKEN_STORAGE_KEY) ?? null
}

function writeToken(token: string | null): void {
  if (token === null) {
    globalThis.localStorage?.removeItem(TOKEN_STORAGE_KEY)
    return
  }
  globalThis.localStorage?.setItem(TOKEN_STORAGE_KEY, token)
}

/**
 * 会话提供者。
 *
 * 它持有三样东西：主体、作用域、已展开的权限码集合。
 * 这三者是前端所有展示裁剪的唯一依据，任何页面都不得自行推导权限。
 */
export function SessionProvider({ children }: { children: ReactNode }): ReactNode {
  const [status, setStatus] = useState<SessionStatus>('loading')
  const [subject, setSubject] = useState<WhoAmIResponse | null>(null)
  const [scope, setScopeState] = useState<string>(
    () => globalThis.localStorage?.getItem(SCOPE_STORAGE_KEY) ?? '',
  )
  const [permissions, setPermissions] = useState<PermissionSet>(() => PermissionSet.empty())
  const [error, setError] = useState<string | null>(null)

  // 用 ref 保存最新的作用域，使 load 的回调身份保持稳定，
  // 避免把 scope 放进依赖数组后每次切换都重新构造整棵子树。
  const scopeRef = useRef(scope)
  scopeRef.current = scope

  const clear = useCallback(() => {
    writeToken(null)
    setSubject(null)
    setPermissions(PermissionSet.empty())
    setStatus('anonymous')
    setError(null)
  }, [])

  /**
   * 采纳服务端解析出的作用域。
   *
   * 只写状态与本地存储，**不触发重新拉取**——它就是本次拉取的结果，
   * 再拉一次会绕成死循环。
   */
  const adoptScope = useCallback((next: string): void => {
    scopeRef.current = next
    setScopeState(next)
    globalThis.localStorage?.setItem(SCOPE_STORAGE_KEY, next)
  }, [])

  /**
   * 拉取会话与权限码。
   *
   * 任何失败都收敛到"未登录"或"错误"，不会留下"有主体但没权限码"的中间态——
   * 那种状态会让界面在"能点"与"点了报错"之间闪烁。失败作为返回值交给调用方，
   * 自己只负责把状态落好（见 LoadOutcome）。
   */
  const load = useCallback(async (): Promise<LoadOutcome> => {
    if (readToken() === null) {
      setStatus('anonymous')
      setSubject(null)
      setPermissions(PermissionSet.empty())
      return { ok: false, message: '尚未登录' }
    }
    try {
      const session = await identityApi.whoAmI()
      // 作用域留空时，服务端会回落到凭证自身绑定的默认作用域，
      // 并把**实际使用的那个**回传回来。
      const effective = await identityApi.getSessionPermissions(scopeRef.current)
      setSubject(session)
      setPermissions(PermissionSet.from(effective.permissions))

      // 请求里的范围留空（等于全局作用域）时服务端**不会**按全局展开，而是回落到
      // 凭证绑定默认作用域——"不降级为全局"是刻意的，否则一次漏传就变成一次越权。
      // 它会把实际使用的那个回传回来。
      //
      // 因此这里必须采纳服务端的结果：权限码集合是按那个范围展开的，界面显示的
      // 范围若与它不一致，就是自相矛盾——菜单因为持有权限码而渲染出来、点进去却被拒。
      // 前端不猜范围——服务端说用了哪个就用哪个。
      //
      // 留空是"**不指定**"，不是"我要全局"：它只可能出现在首次登录（本地还没有
      // 选择）或用户主动清空时，两种情形下服务端的解析结果都是对的。
      if (scopeRef.current === '') {
        adoptScope(effective.scope)
      }

      setStatus('authenticated')
      setError(null)
      return { ok: true }
    } catch (err) {
      const message = messageOf(err)
      setError(message)
      clear()
      return { ok: false, message }
    }
  }, [clear, adoptScope])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    // 传输层遇到 Unauthenticated 时回调这里。
    // 放在传输层而不是每个页面，是为了让"会话失效"只有一种处理方式。
    onUnauthenticated(clear)
  }, [clear])

  // 各条登录路径的差别只在"拿什么去换凭证"，换到之后的动作完全相同：
  // 落盘、再拉一次会话与权限码。抽出来是为了让"登录成功后要做什么"只有一份，
  // 将来加第三条登录路径时不会漏掉其中一步。
  //
  // 采纳之后**必须确认真的登进来了**：load 把失败收敛成状态而不是抛出，所以
  // 失败在这里转成一次异常。重定向登录的回调页据此给出提示，而不是静默把用户
  // 送到首页、再被弹回登录页。
  const adoptToken = useCallback(
    async (accessToken: string): Promise<void> => {
      writeToken(accessToken)
      const outcome = await load()
      if (!outcome.ok) {
        throw new Error(outcome.message)
      }
    },
    [load],
  )

  const refresh = useCallback(async (): Promise<void> => {
    await load()
  }, [load])

  const signIn = useCallback(
    async (token: string, trace?: TraceCapture): Promise<void> => {
      await adoptToken((await identityApi.login(token, trace)).accessToken)
    },
    [adoptToken],
  )

  const setScope = useCallback(
    async (next: string): Promise<void> => {
      setScopeState(next)
      globalThis.localStorage?.setItem(SCOPE_STORAGE_KEY, next)
      scopeRef.current = next
      // 作用域变更等同于权限变更：必须重新拉取权限码集合，
      // 不允许沿用旧集合（见 docs/design/rbac/frontend-permissions.md）。
      setPermissions(PermissionSet.empty())
      await load()
    },
    [load],
  )

  const value = useMemo<SessionState>(
    () => ({
      status,
      subject,
      scope,
      permissions,
      error,
      signIn,
      adoptSessionToken: adoptToken,
      signOut: clear,
      setScope,
      refresh,
    }),
    [
      status,
      subject,
      scope,
      permissions,
      error,
      signIn,
      adoptToken,
      clear,
      setScope,
      refresh,
    ],
  )

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}

/** 取回会话状态。必须在 SessionProvider 内使用。 */
export function useSession(): SessionState {
  const value = useContext(SessionContext)
  if (value === null) {
    throw new Error('useSession 必须在 SessionProvider 内使用')
  }
  return value
}

/** 供传输层读取凭证，避免它反向依赖 React 上下文。 */
export const tokenStorage = { read: readToken, write: writeToken }
