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
 * 开始一次头像上传：服务端签发一份**只对头像这一个键、只允许写、短时有效**
 * 的直传凭证（见 docs/design/objectstore/README.md）。
 *
 * 类型与大小由**上传方声明**，服务端只校验声明在白名单与上限内：直传的服务端
 * 从不接触字节，因此声明是它唯一能据以早退的输入；真正的约束由存储侧执行。
 * 拿到的凭证用完即弃，**不要缓存、不要复用到别的上传**。
 */
export async function beginAvatarUpload(contentType: string, sizeBytes: number) {
  return profileClient().beginAvatarUpload({ contentType, sizeBytes: BigInt(sizeBytes) })
}

/**
 * 提交一次头像上传：服务端对那个键做一次存在性与字节数核对，通过后返回新档案。
 *
 * 参数里没有键，也没有类型——两次调用之间服务端不保留状态，"哪个键"由签发时
 * 分配、提交时按主体重新确认。返回的是完整档案，客户端据此直接更新本地状态。
 */
export async function commitAvatarUpload() {
  return profileClient().commitAvatarUpload({})
}

/** 删除头像。没有头像时也成功。 */
export async function deleteMyAvatar() {
  return profileClient().deleteAvatar({})
}
