import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'

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
  /** 使用凭证登录。 */
  signIn: (token: string) => Promise<void>
  /** 清除本地凭证。 */
  signOut: () => void
  /** 切换作用域并重新拉取权限码。 */
  setScope: (scope: string) => Promise<void>
  /** 重新拉取会话与权限码。 */
  refresh: () => Promise<void>
}

const SessionContext = createContext<SessionState | null>(null)

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
   * 拉取会话与权限码。
   *
   * 任何失败都收敛到"未登录"或"错误"，不会留下"有主体但没权限码"的中间态——
   * 那种状态会让界面在"能点"与"点了报错"之间闪烁。
   */
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

  const load = useCallback(async (): Promise<void> => {
    if (readToken() === null) {
      setStatus('anonymous')
      setSubject(null)
      setPermissions(PermissionSet.empty())
      return
    }
    try {
      const session = await identityApi.whoAmI()
      // 作用域留空时，服务端会回落到凭证自身绑定的默认作用域，
      // 并把**实际使用的那个**回传回来。
      const effective = await identityApi.getSessionPermissions(scopeRef.current)
      setSubject(session)
      setPermissions(PermissionSet.from(effective.permissions))

      // 本地没有显式作用域（首次登录、换了浏览器）时必须采纳服务端的结果。
      //
      // 否则会出现自相矛盾的表现：权限码集合是按 tenant/acme 展开的，
      // 而后续按作用域过滤的接口却带着空作用域（等于全局）发出去，
      // 于是菜单因为持有权限码而渲染出来、点进去却被拒。
      // 前端不猜作用域——服务端说用了哪个就用哪个。
      if (scopeRef.current === '') {
        adoptScope(effective.scope)
      }

      setStatus('authenticated')
      setError(null)
    } catch (err) {
      setError(messageOf(err))
      clear()
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

  const signIn = useCallback(
    async (token: string): Promise<void> => {
      const response = await identityApi.login(token)
      writeToken(response.accessToken)
      await load()
    },
    [load],
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
      signOut: clear,
      setScope,
      refresh: load,
    }),
    [status, subject, scope, permissions, error, signIn, clear, setScope, load],
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
