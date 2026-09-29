import { useCallback, useEffect, useState } from 'react'

import * as rbacApi from '../api/rbac'
import { useAnyPermission, useSession } from '../auth'
import { PermissionCodes } from '../gen/permission-codes'

/** 当前主体自己的绑定范围，用作顶栏「管理范围」的候选项。 */
export interface MyScopes {
  /**
   * 去重后的范围值，全局（空串）排在最前。
   * 拿不到候选时为空——空列表不等于"他没有绑定"，只等于"这次列不出来"。
   */
  scopes: string[]
  /** 候选这次是否可用。false 时界面只提供手输，不把它当成一次失败报出来。 */
  available: boolean
}

/**
 * 取回当前主体绑定到的范围，供顶栏当候选项。
 *
 * 它给出的是**建议**，不是权限边界：列不出来（没有 `rbac.subject.read`、
 * 主体尚未登记、这次读取失败）都不影响手输。因此这里不把错误交给界面——
 * 一个可选列表读不到，不值得在页头摆一条红色告警。
 *
 * 依赖会话主体与当前范围：会话还没就绪时不发请求；范围变化时重取一次，
 * 因为**这个查询本身也要在某个范围上获得鉴权**。
 */
export function useMyScopes(): MyScopes {
  const { subject, scope } = useSession()
  const canRead = useAnyPermission([PermissionCodes.RbacSubjectRead])
  const subjectId = subject?.subjectId ?? ''

  const [scopes, setScopes] = useState<string[]>([])
  const [failed, setFailed] = useState(false)

  const load = useCallback(async (): Promise<void> => {
    if (!canRead || subjectId === '') {
      setScopes([])
      setFailed(false)
      return
    }
    try {
      const response = await rbacApi.listSubjectBindings(scope, subjectId)
      setScopes(uniqueScopes(response.bindings.map((b) => b.scope)))
      setFailed(false)
    } catch {
      setScopes([])
      setFailed(true)
    }
  }, [canRead, subjectId, scope])

  useEffect(() => {
    void load()
  }, [load])

  return { scopes, available: canRead && !failed }
}

/** 去重并稳定排序：全局排最前，其余按字典序。 */
function uniqueScopes(raw: readonly string[]): string[] {
  const seen = new Set(raw)
  return [...seen].sort((a, b) => {
    if (a === b) return 0
    if (a === '') return -1
    if (b === '') return 1
    return a < b ? -1 : 1
  })
}
