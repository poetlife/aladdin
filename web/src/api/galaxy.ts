import type { ContentSlot } from '../gen/proto/aladdin/galaxy/v1/galaxy_pb'
import { traceCallOptions, type TraceCapture } from './call-trace'
import { galaxyClient } from './transport'

/**
 * galaxy 创作面的调用封装。
 *
 * 与 profile.ts 同构：这里只提供有名字的调用入口，类型全部来自 proto 生成，
 * **不做任何业务判断**。判断只有一处实现——"这份草稿能不能发布"由服务端的
 * ValidateDraft 回答（见 docs/design/galaxy/README.md 的"引用完整性的唯一
 * 入口"），前端不复写引用解析。
 *
 * **这里没有任何写内容的入口。** 内容的写入只有命令行一条路（push 表达整组的
 * 期望状态）；两个入口并存会引出"网页上刚改的一句被一次 push 静默盖掉"这类只在
 * 两个入口之间发生的冲突，收成一条路径，那份冲突连同它需要的基线校验一起不存在
 * （见 docs/design/galaxy/authoring.md）。
 *
 * 所有方法的目标工程都取自参数里的工程标识，而**归属由凭证决定**：接口面上
 * 没有"指定拥有者"这个形状，因此这里也没有接收拥有者的参数。
 */

/** 读取当前部署下创作能力的边界（能不能传素材、能不能发布、各项上限）。 */
export async function getCapabilities() {
  return galaxyClient().getCapabilities({})
}

/**
 * 列出调用者自己的工程。
 *
 * `trace` 是这次调用的链路标识捕获（见 ./call-trace）：传了它，调用方在返回后
 * 就能拿到服务端为**这一次**请求盖的 trace_id。只有"要拿它去上报一条客户端事件"
 * 的调用才需要传——不是为了遥测的调用传它没有意义。
 */
export async function listProjects(trace?: TraceCapture) {
  return galaxyClient().listProjects({}, traceCallOptions(trace))
}

/**
 * 创建一个工程。标识由服务端分配。
 *
 * slots 是创建时要启用的**内容槽**（`site` / `docs`），**至少一个**：一个工程
 * 可以既是站点又有文档，两者各有一套草稿、版本与发布地址，互不影响。
 */
export async function createProject(name: string, description: string, slots: ContentSlot[]) {
  return galaxyClient().createProject({ name, description, slots })
}

/**
 * 给一个已有工程加一个内容槽。
 *
 * **单向**：没有"删掉一个槽"的对应调用。加一个槽不改变另一个槽。
 */
export async function addProjectSlot(projectId: string, slot: ContentSlot) {
  return galaxyClient().addProjectSlot({ projectId, slot })
}

/**
 * 读取一个工程的元数据。不含草稿、版本与产物清单。
 *
 * `trace` 的含义与 listProjects 同：给需要上报客户端事件的调用方回填这次请求的
 * 链路标识。"打开工作台 / 打开独立预览页"这两条事件都取这次调用——它是它们各自
 * 加载流程里的第一次请求（见 ./call-trace 的 traceIdForAction）。
 */
export async function getProject(projectId: string, trace?: TraceCapture) {
  return galaxyClient().getProject({ projectId }, traceCallOptions(trace))
}

/** 修改工程的名称与简介。空串表示清空。**内容槽不在其中**（槽只增不删）。 */
export async function updateProject(projectId: string, name: string, description: string) {
  return galaxyClient().updateProject({ projectId, name, description })
}

/** 删除一个工程。连带删除它的全部内容槽、版本、资产与发布记录。 */
export async function deleteProject(projectId: string) {
  return galaxyClient().deleteProject({ projectId })
}

/** 读取**某一个槽**的当前草稿清单，每一项带一条短时读取地址。 */
export async function getDraft(projectId: string, slot: ContentSlot) {
  return galaxyClient().getDraft({ projectId, slot })
}

/**
 * 把**某一个槽**草稿的当前清单保存成一个不可变版本。
 *
 * `description` 是这一版的一句说明（可留空，之后也能改）。`fromSnapshotId` 非空时
 * 存的是**那条草稿历史**的清单，而不是当前草稿——它回答的是"我想把那次中间态正式
 * 记下来"。
 *
 * 注意 **`-m` 隐含 `--save` 是命令行那边的规矩，不在这一层**：这里两个入参各管
 * 各的，传了说明就是要有说明。
 */
export async function saveVersion(
  projectId: string,
  slot: ContentSlot,
  description = '',
  fromSnapshotId = '',
) {
  return galaxyClient().saveVersion({ projectId, slot, description, fromSnapshotId })
}

/**
 * 改一个版本的**说明**。
 *
 * 它只动说明那一层：清单、序号、保存时间与渲染规则版本都不变，因此已经发出去的
 * 页面不受影响（见 docs/design/galaxy/project-versioning.md）。空串清空说明。
 */
export async function updateVersion(
  projectId: string,
  slot: ContentSlot,
  versionId: string,
  description: string,
) {
  return galaxyClient().updateVersion({ projectId, slot, versionId, description })
}

/**
 * 列出**某一个槽**的草稿历史：被替换掉的那些旧清单，最近的在前。
 *
 * 它们**不是版本**：没有序号、不能发布、按保留策略过期（每个槽最近 50 条、且不
 * 超过 14 天）。它是"只推不存版本"这条用法的兜底。
 */
export async function listDraftSnapshots(projectId: string, slot: ContentSlot) {
  return galaxyClient().listDraftSnapshots({ projectId, slot })
}

/**
 * 把草稿**整组换回**某一条草稿历史的清单。
 *
 * **恢复本身也留一条历史**（它走的是同一处草稿替换），因此恢复不会丢掉恢复前的
 * 内容。引用了已删除资产的那条历史不能恢复，服务端会拒绝并指出是哪一处。
 */
export async function restoreDraftSnapshot(projectId: string, slot: ContentSlot, snapshotId: string) {
  return galaxyClient().restoreDraftSnapshot({ projectId, slot, snapshotId })
}

/** 列出**某一个槽**的版本，按序号排序。清单随行，但不带读取地址。 */
export async function listVersions(projectId: string, slot: ContentSlot) {
  return galaxyClient().listVersions({ projectId, slot })
}

/** 读取一个版本，含清单与每一项的短时读取地址。 */
export async function getVersion(projectId: string, slot: ContentSlot, versionId: string) {
  return galaxyClient().getVersion({ projectId, slot, versionId })
}

/** 删除一个版本。被**它所属槽**的发布指向时会被服务端拒绝。 */
export async function deleteVersion(projectId: string, slot: ContentSlot, versionId: string) {
  return galaxyClient().deleteVersion({ projectId, slot, versionId })
}

/** 校验**某一个槽当前草稿的清单**能不能发布。界面提示与发布前置校验共用这一个入口。 */
export async function validateDraft(projectId: string, slot: ContentSlot) {
  return galaxyClient().validateDraft({ projectId, slot })
}

/**
 * 给出某一个槽草稿的预览入口地址（带短时凭证）。
 *
 * **渲染在服务端**，与发布共用同一段实现：`docs` 槽的 markdown → HTML 只有
 * 一处实现，前端不再引第二个渲染器——两份实现迟早漂移，而用户看到的是"预览好好
 * 的、发布出来不一样"。
 *
 * path 是要预览的那一份（`site` 用它换一页）；为空表示入口。`docs` 槽的页面由
 * 服务端渲染，因此给它的产物路径也可以。
 */
export async function previewDraft(projectId: string, slot: ContentSlot, path = '') {
  return galaxyClient().previewDraft({ projectId, slot, path })
}

/**
 * 列出工程资产库里的资产，含短时有效的读取地址。
 *
 * tags 非空时按标签筛选（精确匹配、多值取交集）。响应里另带一份**整个工程**
 * 已有的标签（`projectTags`），供筛选界面做候选——它不随本次筛选收窄。
 */
export async function listAssets(projectId: string, tags: string[] = []) {
  return galaxyClient().listAssets({ projectId, tags })
}

/**
 * 开始一次资产上传：服务端分配一个资产标识并签发一份**只对那一个键、只允许
 * 写、短时有效**的直传凭证（见 docs/design/objectstore/README.md）。
 *
 * content_type 与 size_bytes 都是**上传方声明**：直传路径上服务端没有字节，
 * 声明是它据以早退、签发策略的输入；类型必须在白名单内、大小不得超过对应
 * 类别的上限，真正的约束由存储侧按声明执行。声明**不是**内容嗅探。
 */
export async function beginAssetUpload(projectId: string, contentType: string, sizeBytes: number) {
  return galaxyClient().beginAssetUpload({
    projectId,
    contentType,
    sizeBytes: BigInt(sizeBytes),
  })
}

/**
 * 提交一次资产上传。
 *
 * digest 是文件字节的 **SHA-256 十六进制**（见 upload/content-digest.ts），
 * 由浏览器算好带上来——它是公开区地址的键，服务端没有字节可以算它。服务端在
 * 提交时不校验它，但会在发布时读回对象核对，所以**必须算对**。
 *
 * filename 只是一个展示标签，不进对象键、不参与任何判断。
 *
 * title / notes / tags 是**可选的说明层元数据**，随这次提交一并写入：一次带走
 * 省掉一次往返，结果与"提交之后再调 updateAsset"完全相同。标签由服务端归一化。
 */
export async function commitAssetUpload(
  projectId: string,
  assetId: string,
  contentType: string,
  digest: string,
  filename: string,
  title = '',
  notes = '',
  tags: string[] = [],
) {
  return galaxyClient().commitAssetUpload({ projectId, assetId, contentType, digest, filename, title, notes, tags })
}

/**
 * 改资产的展示标题、标签与备注。
 *
 * 表达的是**期望的完整状态**：空串清空标题或备注，标签整体替换。
 *
 * 它只动**说明层**（见 docs/design/galaxy/asset-library.md）：字节、内容摘要、
 * 媒体类型与对象键都不变，已发布的页面也不会因此变化。
 */
export async function updateAsset(
  projectId: string,
  assetId: string,
  title: string,
  tags: string[],
  notes: string,
) {
  return galaxyClient().updateAsset({ projectId, assetId, title, tags, notes })
}

/** 删除一个资产。被任一版本引用时会被服务端拒绝。 */
export async function deleteAsset(projectId: string, assetId: string) {
  return galaxyClient().deleteAsset({ projectId, assetId })
}

/** 发布**某一个槽**的一个版本。只能发布版本，不能发布草稿。 */
export async function publish(projectId: string, slot: ContentSlot, versionId: string) {
  return galaxyClient().publish({ projectId, slot, versionId })
}

/** 撤回**某一个槽**的发布。该槽地址立刻不可达；另一个槽不受影响；发布记录保留。 */
export async function unpublish(projectId: string, slot: ContentSlot) {
  return galaxyClient().unpublish({ projectId, slot })
}

/**
 * 把一条**主站分享路径**解析成它在发布域上的内容地址（主站壳用）。
 *
 * 它是唯一一个**匿名可用**的 galaxy 调用：访客打开一条分享地址时没有会话，而
 * "分享给没登录的人"正是它的用途。服务端对未发布 / 已撤回 / 槽未启用 / 工程不
 * 存在 / 标识没被猜中 / 路径不在集合，一律返回空 `contentUrl`——**那不是错误，
 * 是统一的否定结论**，调用方据此渲染"页面不存在"（见
 * docs/design/galaxy/publication.md 的"主站壳"）。
 *
 * 传整条路径而不是（工程，槽，路径）三元组：槽的判定只有服务端一处，前端不做
 * 第二份形状解析。
 */
export async function resolveSharedPage(path: string) {
  return galaxyClient().resolveSharedPage({ path })
}
