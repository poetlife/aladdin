import type { PermissionCode } from '../gen/permission-codes'

/**
 * 已展开的权限码集合。
 *
 * 集合成员判断是本文件唯一做的事——**不实现**继承、通配与作用域包含。
 * 那些逻辑在服务端展开时已经完成（见 docs/design/rbac/frontend-permissions.md），
 * 前端重复实现它们必然与服务端产生分歧，而分歧的表现形式是"能点但点了报错"。
 */
export class PermissionSet {
  private readonly codes: ReadonlySet<string>

  private constructor(codes: ReadonlySet<string>) {
    this.codes = codes
  }

  /** 由服务端返回的权限码数组构造。 */
  static from(codes: readonly string[]): PermissionSet {
    return new PermissionSet(new Set(codes))
  }

  /** 空集合。未登录或未加载完成时使用。 */
  static empty(): PermissionSet {
    return new PermissionSet(new Set())
  }

  /** 报告是否持有指定权限码。 */
  has(code: PermissionCode): boolean {
    return this.codes.has(code)
  }

  /** 报告是否持有其中任意一个。 */
  hasAny(codes: readonly PermissionCode[]): boolean {
    return codes.some((code) => this.codes.has(code))
  }

  /** 报告是否持有全部。 */
  hasAll(codes: readonly PermissionCode[]): boolean {
    return codes.every((code) => this.codes.has(code))
  }

  /** 返回原始权限码列表，仅用于展示与排查。 */
  toArray(): string[] {
    return [...this.codes].sort()
  }

  /** 集合是否为空。 */
  get isEmpty(): boolean {
    return this.codes.size === 0
  }
}
