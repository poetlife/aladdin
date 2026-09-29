import { describe, expect, it } from 'vitest'

import { projectTopic } from './topics'

// **主题的字面量是两个语言之间的约定**：前端这一侧由本模块拼，服务端那一侧由
// `galaxy.ProjectTopic` 拼。两边各有一条测试钉住同一个字符串（服务端见
// `internal/galaxy` 的 `TestProjectTopicShape`），任何一侧改了它，那一侧的测试
// 立刻失败——否则两边会安静地各说各话，而表现是"订阅了却永远收不到"。
describe('订阅主题的形状', () => {
  it('工程主题是 <类型>/<资源标识>', () => {
    expect(projectTopic('prj_abc')).toBe('galaxy.project/prj_abc')
  })
})
