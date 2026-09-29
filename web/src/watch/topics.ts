/**
 * 主题的形状与取值（前端侧唯一入口）。
 *
 * 服务端那侧的形状定义在 `internal/watch/topic.go` 的 `Topic()`，各资源的类型名由
 * 属主模块给出（galaxy 的是 `internal/galaxy/events.go` 的 `ProjectTopicKind`）。
 * 与直传凭证同源：**跨语言共享的是形状，代码各一份**，而形状只能有一种——因此
 * 这里拼主题的规则与类型名都收在这一个文件里，别处不得再拼一遍。
 *
 * 两侧的字面量各由一条测试钉住（前端见 `topics.test.ts`，服务端见
 * `internal/galaxy` 的 `TestProjectTopicShape`）：任何一侧改了这个字符串，
 * 它自己那侧的测试立刻失败。
 */

/** galaxy 工程主题的类型名。 */
const PROJECT_KIND = 'galaxy.project'

/** 一个工程的订阅主题。 */
export function projectTopic(projectId: string): string {
  return `${PROJECT_KIND}/${projectId}`
}
