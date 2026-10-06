import type { Skill } from '../gen/proto/aladdin/skill/v1/skill_pb'
import { directUpload } from '../upload/direct-upload'
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

/**
 * 从远端纳管一个技能。地址只接受 github.com 的仓库根形状。
 *
 * 五项可选项都显式接受 `undefined`（`exactOptionalPropertyTypes` 下 `?` 单用不
 * 允许显式传 undefined）：表单里没填的项 validateFields 给的正是 undefined，
 * 而这里对"没给"与"给了 undefined"本来就一视同仁——都回落到服务端的默认取值。
 */
export async function importSkill(input: {
  repositoryUrl: string
  ref?: string | undefined
  subPath?: string | undefined
  title?: string | undefined
  summary?: string | undefined
  tags?: string[] | undefined
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

/**
 * 开始一次封面上传：签发一份直传凭证。
 *
 * **字节不经过服务端**：服务端只校验声明的类型与大小，真正的边界由存储侧按策略
 * 执行。三步的顺序见 updateCover。
 */
export async function beginCoverUpload(skillId: string, contentType: string, sizeBytes: number) {
  return skillAdminClient().beginSkillCoverUpload({
    skillId,
    contentType,
    sizeBytes: BigInt(sizeBytes),
  })
}

/** 提交一次封面上传：核对对象确实到了，把技能指向它。 */
export async function commitCoverUpload(skillId: string) {
  return skillAdminClient().commitSkillCoverUpload({ skillId })
}

/** 移除封面。没有封面时也成功（幂等）。 */
export async function deleteCover(skillId: string) {
  return skillAdminClient().deleteSkillCover({ skillId })
}

/**
 * 换一张封面：**三步走完**——签发 → 直传 → 提交。
 *
 * 三步收在一处，是因为漏掉任何一步都会留下一个说不清的状态：只签发不提交是"桶上
 * 有对象、技能没指向它"，只提交不直传是"技能指向一个空键"。凭证**用完即弃**，
 * 不缓存、也不复用到别的上传（见 ../upload/direct-upload.ts）。
 */
export async function updateCover(skillId: string, file: File): Promise<Skill> {
  const begin = await beginCoverUpload(skillId, file.type, file.size)
  if (begin.upload === undefined) {
    throw new Error('服务端没有返回直传凭证')
  }
  await directUpload(begin.upload, file, file.type)
  // 提交回的是**服务端的那一份**：据此刷新，而不是在本地猜封面地址。
  const done = await commitCoverUpload(skillId)
  return done.skill!
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
