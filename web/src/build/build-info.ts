/**
 * 本页面是哪一次构建产出的。
 *
 * 三项取值由构建期注入（vite 的 `define`，见 vite.config.ts），与后端
 * `internal/buildinfo` 来自**同一处取值**——同一个 VERSION / COMMIT / BUILD_TIME
 * 由 Makefile 一处传给两边。因此部署信息页上"前后端提交号不一致"是一个可信的
 * 结论，而不是两个不相干字符串的比较（见 docs/design/deployment/README.md）。
 *
 * 这三项在开发服务器上是 dev / 空 / 空：不经 Makefile 的构建没有值可注入，
 * 此时页面显示"未注入"，而不是编一个看起来像真的版本号。
 */

// 三个全局由 vite 的 define 做**文本替换**。它们不挂在 window 上，也不来自
// import.meta.env——define 收到的名字必须与这里声明的一字不差，写错不报错，
// 只在运行时抛 ReferenceError。
declare const __BUILD_VERSION__: string
declare const __BUILD_COMMIT__: string
declare const __BUILD_TIME__: string

/** 一次构建的自述，与后端 buildinfo 的前三项一一对应。 */
export interface BuildInfo {
  /** 版本号：发布构建为 tag，本机构建为 git describe 的结果，未经 Makefile 为 dev。 */
  version: string
  /** 提交号；空表示构建期没有注入。 */
  commit: string
  /** 构建时刻，RFC3339；空表示构建期没有注入。 */
  buildTime: string
}

/** 本次前端构建的自述。 */
export const buildInfo: BuildInfo = {
  version: __BUILD_VERSION__,
  commit: __BUILD_COMMIT__,
  buildTime: __BUILD_TIME__,
}

/** 前后端提交号的对照结论。 */
export type CommitAgreement =
  /** 两侧都有提交号，且相同：页面与服务端出自同一次构建。 */
  | 'same'
  /** 两侧都有提交号，但不相同：前端缓存了旧 bundle，或前后端是分开部署的。 */
  | 'different'
  /**
   * 至少一侧没有提交号，无从比较。
   *
   * 与 `different` 是两回事，**必须分开**：本机构建、不经 Makefile 的构建都拿不到
   * 提交号，把它们报成"不一致"会在每次本地开发时给出一个假的告警——而一个平时
   * 总在报警的提示，在真的不一致那天也就没人看了。
   */
  | 'unknown'

/**
 * 比较前后端的提交号。
 *
 * 两侧的值都原样给进来，这里不自己取：前端那一侧来自 {@link buildInfo}，后端
 * 那一侧来自 GetDeploymentInfo 的响应，取值方式在调用处一目了然。
 */
export function compareCommits(frontend: string, backend: string): CommitAgreement {
  if (frontend === '' || backend === '') {
    return 'unknown'
  }
  return frontend === backend ? 'same' : 'different'
}
