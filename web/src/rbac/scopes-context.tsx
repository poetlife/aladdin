import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'

import * as rbacApi from '../api/rbac'
import { useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'
import type { Scope } from '../gen/proto/aladdin/rbac/v1/rbac_pb'

/** 部署里已登记的范围（不含全局：它是模型的根，不是目录里的一条）。 */
export interface ScopesState {
  /** 已登记的范围。读不到（没权限、读取失败）时为空。 */
  scopes: Scope[]
  /** 有没有读范围目录的权限。false 时 `scopes` 恒为空，且不是一次失败。 */
  canRead: boolean
  /** 正在拉取。列表页要据此显示加载态，而不是把"还没到"显示成"一个都没有"。 */
  loading: boolean
  /**
   * 上次读取的失败原因，没失败时为 null。**原样交给调用方**：顶栏把它当成
   * "候选没了"就够了，而范围页要把它说成人话并附上追踪 ID。
   */
  error: unknown
  /** 重新拉取。**改动过范围目录的地方必须调它**，否则别处还拿着旧的。 */
  reload: () => Promise<void>
}

const ScopesContext = createContext<ScopesState | null>(null)

/**
 * 范围目录的提供者。
 *
 * 它由外壳持有，而不是每个用到的地方各拉一份：**同一条事实只能有一份状态**。
 * 顶栏要拿它当候选、人员授权页要拿它当授予目标、范围页要拿它当列表——三处各拉
 * 一份时，范围页刚建的范围不会出现在顶栏那份里，界面自相矛盾。
 * 档案（ProfileProvider）是同一个形状：外壳与页面共读一份，改完立刻同步。
 *
 * 拉不到（没有 `rbac.scope.read`、这次读取失败）不算错误：候选是便利，不是边界，
 * 因此不抛给界面，由 `available` 让调用方决定怎么退化。
 */
export function ScopesProvider({ children }: { children: ReactNode }): ReactNode {
  const { scope } = useSession()
  // 没有读范围目录的权限时不发这个请求：它注定被拒，而在日志里留下一次 403
  // 只会让人以为哪里坏了。候选是便利，不是边界。
  const canRead = useAnyPermission([PermissionCodes.RbacScopeRead])

  const [scopes, setScopes] = useState<Scope[]>([])
  const [error, setError] = useState<unknown>(null)
  const [loading, setLoading] = useState(false)

  const reload = useCallback(async (): Promise<void> => {
    if (!canRead) {
      setScopes([])
      setError(null)
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const response = await rbacApi.listScopes(scope)
      setScopes(response.scopes)
      setError(null)
    } catch (err) {
      setScopes([])
      setError(err)
    } finally {
      setLoading(false)
    }
  }, [canRead, scope])

  useEffect(() => {
    void reload()
  }, [reload])

  const value = useMemo<ScopesState>(
    () => ({ scopes, canRead, loading, error, reload }),
    [scopes, canRead, loading, error, reload],
  )

  return <ScopesContext.Provider value={value}>{children}</ScopesContext.Provider>
}

/** 取回范围目录。必须在 ScopesProvider 内使用。 */
export function useScopes(): ScopesState {
  const value = useContext(ScopesContext)
  if (value === null) {
    throw new Error('useScopes 必须在 ScopesProvider 内使用')
  }
  return value
}
