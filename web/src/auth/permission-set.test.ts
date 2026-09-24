import { describe, expect, it } from 'vitest'

import { PermissionSet } from './permission-set'
import { PermissionCodes } from '../gen/permission-codes'

describe('PermissionSet', () => {
  it('空集合不持有任何权限', () => {
    const set = PermissionSet.empty()
    expect(set.isEmpty).toBe(true)
    expect(set.has(PermissionCodes.RbacRoleRead)).toBe(false)
  })

  it('按等值判断持有关系', () => {
    const set = PermissionSet.from([PermissionCodes.RbacRoleRead])
    expect(set.has(PermissionCodes.RbacRoleRead)).toBe(true)
    expect(set.has(PermissionCodes.RbacRoleWrite)).toBe(false)
  })

  it('服务端展开后的通配符是普通取值，前端不做解释', () => {
    // 服务端把 "*" 作为已展开的最终权限码下发；前端只做集合成员判断，
    // 不实现任何通配匹配——那是服务端的职责。
    const set = PermissionSet.from(['*'])
    expect(set.has(PermissionCodes.RbacRoleRead)).toBe(false)
    expect(set.toArray()).toEqual(['*'])
  })

  it('hasAny 与 hasAll', () => {
    const set = PermissionSet.from([PermissionCodes.RbacRoleRead, PermissionCodes.AuditLogRead])
    expect(set.hasAny([PermissionCodes.RbacRoleWrite, PermissionCodes.AuditLogRead])).toBe(true)
    expect(set.hasAny([PermissionCodes.RbacRoleWrite])).toBe(false)
    expect(set.hasAll([PermissionCodes.RbacRoleRead, PermissionCodes.AuditLogRead])).toBe(true)
    expect(set.hasAll([PermissionCodes.RbacRoleRead, PermissionCodes.RbacRoleWrite])).toBe(false)
  })

  it('空数组语义：hasAny 为假、hasAll 为真', () => {
    const set = PermissionSet.from([PermissionCodes.RbacRoleRead])
    expect(set.hasAny([])).toBe(false)
    expect(set.hasAll([])).toBe(true)
  })

  it('toArray 去重并排序', () => {
    const set = PermissionSet.from([
      PermissionCodes.RbacRoleWrite,
      PermissionCodes.RbacRoleRead,
      PermissionCodes.RbacRoleRead,
    ])
    expect(set.toArray()).toEqual([PermissionCodes.RbacRoleRead, PermissionCodes.RbacRoleWrite])
  })
})
