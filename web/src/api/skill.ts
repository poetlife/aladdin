import type { Skill } from '../gen/proto/aladdin/skill/v1/skill_pb'
import { skillAdminClient, skillClient } from './transport'

/**
 * 技能目录的调用封装。
 *
 * 与 galaxy.ts 同构：只提供有名字的调用入口，类型全部来自 proto 生成，**不做任何
 * 业务判断**。有效标题的回退、标签的归一化、排序与使用量的口径都在服务端算完，
 * 前端只显示（见 docs/design/skill/catalog.md）。
 *
 * **这里没有任何一条会写文件的路径。** 平台从不往调用方的文件系统写东西，网页
 * 侧的表现就是"只显示与引用，没有下载/安装"（见 docs/design/skill/agent-access.md）。
 */

/** 读取当前部署下技能目录的边界（能不能用、纳得进多大的包）。 */
export async function getCapabilities() {
  return skillClient().getCapabilities({})
}

/**
 * 列出 / 搜索技能。
 *
 * query 是大小写不敏感的子串匹配（匹配有效标题与有效简介）；tags 多值**取交集**；
 * favoritedOnly 只看自己收藏的。排序由服务端定死，前端不再排一次。
 */
export async function listSkills(query = '', tags: string[] = [], favoritedOnly = false) {
  return skillClient().listSkills({ query, tags, favoritedOnly })
}

/** 读取一个技能的详情：说明层、来源、文件清单与回退之后的展现取值。**不含正文**。 */
export async function getSkill(skillId: string) {
  return skillClient().getSkill({ skillId })
}

/**
 * 取一条文件的字节。
 *
 * **它计一次使用**（取用是使用量的计量点），因此"翻一翻"与"用一下"是两件事：
 * 详情页只在用户真去读某一份文件时才调它。
 */
export async function getSkillFile(skillId: string, path: string) {
  return skillClient().getSkillFile({ skillId, path })
}

/** 列出这个技能的版本，最新的在前。 */
export async function listSkillVersions(skillId: string) {
  return skillClient().listSkillVersions({ skillId })
}

/** 收藏或取消收藏。请求给出的是期望的完整状态，两个方向都幂等。 */
export async function setFavorite(skillId: string, favorited: boolean) {
  return skillClient().setSkillFavorite({ skillId, favorited })
}

/** 从远端纳管一个技能。地址只接受 github.com 的仓库根形状。 */
export async function importSkill(input: {
  repositoryUrl: string
  ref?: string
  subPath?: string
  title?: string
  summary?: string
  tags?: string[]
}): Promise<Skill> {
  const resp = await skillAdminClient().importSkill({
    repositoryUrl: input.repositoryUrl,
    ref: input.ref ?? '',
    subPath: input.subPath ?? '',
    title: input.title ?? '',
    summary: input.summary ?? '',
    tags: input.tags ?? [],
  })
  return resp.skill!
}

/** 追远端：有新提交就产生新版本并生效。changed 为假表示远端没有变化。 */
export async function resyncSkill(skillId: string) {
  return skillAdminClient().resyncSkill({ skillId })
}

/** 把当前指针切到某个已有版本。回滚只切指针，不改任何字节。 */
export async function setCurrentVersion(skillId: string, versionId: string) {
  return skillAdminClient().setCurrentSkillVersion({ skillId, versionId })
}

/** 改说明层。写入表达的是期望的完整状态：空串清空，标签整体替换。 */
export async function updateMetadata(skillId: string, title: string, summary: string, tags: string[]) {
  return skillAdminClient().updateSkillMetadata({ skillId, title, summary, tags })
}

/** 删除一个技能。**不可逆**：版本、标签、收藏与使用记录一起消失。 */
export async function deleteSkill(skillId: string) {
  return skillAdminClient().deleteSkill({ skillId })
}
