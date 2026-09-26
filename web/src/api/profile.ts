import { profileClient } from './transport'

/**
 * 个人档案面的调用封装。
 *
 * 四个方法都只作用于**当前凭证代表的主体**：接口面上不存在"以某个指定主体
 * 为目标"的形状，因此这里也没有任何接收主体标识的参数。
 *
 * display_name 与 avatar_url 都由服务端算好，前端**不得**自行拼接或推导——
 * 回退规则与地址签发各只有一处实现（见 docs/ssot-registry.md）。
 */

/** 读取当前主体的档案。 */
export async function getMyProfile() {
  return profileClient().getProfile({})
}

/**
 * 设置昵称与简介。
 *
 * 请求表达的是**期望的完整状态**，不是增量修改：空串表示清空该项。
 */
export async function updateMyProfile(nickname: string, bio: string) {
  return profileClient().updateProfile({ nickname, bio })
}

/**
 * 上传或替换头像。
 *
 * 这里**不声明内容类型**：类型由服务端从字节本身判定。让上传方声明等于把
 * 一个安全属性交给它自证（见 docs/design/profile/avatar-storage.md）。
 */
export async function updateMyAvatar(image: Uint8Array) {
  return profileClient().updateAvatar({ image })
}

/** 删除头像。没有头像时也成功。 */
export async function deleteMyAvatar() {
  return profileClient().deleteAvatar({})
}
