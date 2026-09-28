import { createContext, useCallback, useContext, useEffect, useState } from 'react'

import * as profileApi from '../api/profile'
import { messageOf } from '../api/errors'
import type { Profile } from '../gen/proto/aladdin/profile/v1/profile_pb'
import { directUpload } from '../upload/direct-upload'

/**
 * 当前主体的档案在客户端的唯一副本。
 *
 * 它**不放进 SessionProvider**：会话状态回答的是"我是谁、我能做什么"，
 * 与后端 identity / rbac 两个模块的边界一一对应；档案是第三件事。
 * 分开也让"档案读取失败"不至于把登录状态一起拖坏。
 *
 * 展示名（displayName）与头像地址（avatarUrl）**都由服务端算好**，
 * 这里只负责保存与刷新，不做任何拼接或推导。
 */
export interface ProfileState {
  /** 服务端下发的档案；null 表示还没读到。 */
  profile: Profile | null
  /** 首次读取是否还在进行中。 */
  loading: boolean
  /** 首次读取失败的原因；null 表示没出错。 */
  error: string | null
  /** 重新读取档案。 */
  reload: () => Promise<void>
  /**
   * 设置昵称与简介。空串表示清空该项。
   *
   * 失败时**抛出**错误，由调用方决定怎么呈现：同一段失败在不同页面上
   * 该说的话不一样。
   */
  updateProfile: (nickname: string, bio: string) => Promise<void>
  /**
   * 上传或替换头像。
   *
   * 走三步：向服务端取一份直传凭证 → 用凭证把字节直传到对象存储 → 提交，
   * 让服务端核对（见 docs/design/objectstore/README.md）。**类型由上传方声明**，
   * 用 file.type；调用方不负责白名单判断，服务端是权威。
   *
   * 失败时**抛出**错误：直传失败与提交失败都可能发生，由调用方决定怎么呈现，
   * 并给用户一次重试的机会。
   */
  updateAvatar: (file: File) => Promise<void>
  /** 删除头像。没有头像时也成功。 */
  deleteAvatar: () => Promise<void>
}

const ProfileContext = createContext<ProfileState | null>(null)

/**
 * 档案提供者。挂在应用外壳内部——外壳只对已认证用户渲染，
 * 因此这里不需要处理"未登录"这一分支。
 */
export function ProfileProvider({ children }: { children: React.ReactNode }): React.ReactNode {
  const [profile, setProfile] = useState<Profile | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    try {
      const response = await profileApi.getMyProfile()
      setProfile(response.profile ?? null)
      setError(null)
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  // 三个写操作都用**服务端返回的那一份**更新本地状态，而不是把提交的值拼进去：
  // display_name 与 avatar_url 都是服务端算的，拼一份一定会在"清空昵称"这类
  // 情形上与服务端不一致，而那种不一致的表现是"页头显示的名字和我刚填的不一样"。
  const updateProfile = useCallback(async (nickname: string, bio: string) => {
    const response = await profileApi.updateMyProfile(nickname, bio)
    setProfile(response.profile ?? null)
  }, [])

  // 三步：签发 → 直传 → 提交。凭证用完即弃，不缓存、不复用到别的上传。
  // 提交返回的是**服务端的那一份档案**，据此更新本地状态。
  const updateAvatar = useCallback(async (file: File) => {
    const begin = await profileApi.beginAvatarUpload(file.type, file.size)
    if (begin.upload === undefined) {
      throw new Error('服务端没有返回直传凭证')
    }
    await directUpload(begin.upload, file, file.type)
    const response = await profileApi.commitAvatarUpload()
    setProfile(response.profile ?? null)
  }, [])

  const deleteAvatar = useCallback(async () => {
    const response = await profileApi.deleteMyAvatar()
    setProfile(response.profile ?? null)
  }, [])

  return (
    <ProfileContext.Provider
      value={{ profile, loading, error, reload, updateProfile, updateAvatar, deleteAvatar }}
    >
      {children}
    </ProfileContext.Provider>
  )
}

/** 取用当前主体的档案。在 ProfileProvider 之外调用会抛出。 */
export function useProfile(): ProfileState {
  const state = useContext(ProfileContext)
  if (state === null) {
    throw new Error('useProfile 必须在 ProfileProvider 内部使用')
  }
  return state
}
