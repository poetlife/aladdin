import { describe, expect, it } from 'vitest'
// **测试专用的比对物，不是运行时依赖。** 浏览器那一侧的上传用的是官方
// `cos-js-sdk-v5`，而调用面在 node 里按官方算法自己算签名（见 cos-signature.ts）。
// 两份实现算出来的 Authorization 必须逐字相同，否则表现是"上传被存储侧拒绝"，
// 而那句话里看不出是签名错了。这个包里因此把官方 SDK 当作**唯一的对照物**挂在
// devDependencies 上，只在测试里调它的 getAuth。
// eslint-disable-next-line @typescript-eslint/no-require-imports
import COS from 'cos-js-sdk-v5'

import { cosAuthorization, cosSignTime } from './cos-signature.js'

const BUCKET = 'b-1250000000'
const REGION = 'ap-guangzhou'
const HOST = `${BUCKET}.cos.${REGION}.myqcloud.com`
const SECRET_ID = 'AKIDEXAMPLE'
const SECRET_KEY = 'SECRETEXAMPLE'
const SESSION_TOKEN = 'SESSIONTOKEN'

/** 官方 SDK 在同样的输入下算出的那一条 Authorization。 */
function oracle(key: string, headers: Record<string, string>): { auth: string; signTime: string } {
  // 只给三元组，不给 Bucket / Region：后者会让 SDK 自己补一个 host 头，
  // 而我们要的是"参与签名的头与真正发出去的头逐字相同"这一件事由测试自己说清。
  const cos = new COS({
    SecretId: SECRET_ID,
    SecretKey: SECRET_KEY,
    SecurityToken: SESSION_TOKEN,
  })
  const auth = cos.getAuth({
    Method: 'PUT',
    Key: `/${key}`,
    Headers: headers,
    Expires: 3600,
  })
  const signTime = /q-sign-time=([^&]*)/.exec(auth)?.[1]
  if (signTime === undefined) {
    throw new Error(`官方 SDK 没有给出签名时间：${auth}`)
  }
  return { auth, signTime }
}

describe('COS 请求签名', () => {
  it('与官方 SDK 逐字相同（只有 host）', () => {
    const key = 'galaxy/prj_abc/assets/image/ast_xyz'
    const { auth, signTime } = oracle(key, { host: HOST })

    expect(cosAuthorization({ secretID: SECRET_ID, secretKey: SECRET_KEY, method: 'PUT', key, headers: { host: HOST }, signTime })).toBe(auth)
  })

  it('与官方 SDK 逐字相同（含安全令牌与禁止覆盖）', () => {
    const key = 'galaxy/prj_abc/text/9f2c1d'
    const headers = {
      host: HOST,
      'x-cos-security-token': SESSION_TOKEN,
      'x-cos-forbid-overwrite': 'true',
    }
    const { auth, signTime } = oracle(key, headers)

    expect(cosAuthorization({ secretID: SECRET_ID, secretKey: SECRET_KEY, method: 'PUT', key, headers, signTime })).toBe(auth)
  })

  it('头的顺序不影响结果，但列进来的头必须都被校验', () => {
    const key = 'galaxy/prj_abc/assets/audio/ast_a'
    const headers = { 'x-cos-security-token': SESSION_TOKEN, host: HOST }
    const { auth: ordered } = oracle(key, { host: HOST, 'x-cos-security-token': SESSION_TOKEN })
    const { auth: reversed, signTime } = oracle(key, headers)

    expect(ordered).toBe(reversed)
    expect(cosAuthorization({ secretID: SECRET_ID, secretKey: SECRET_KEY, method: 'PUT', key, headers, signTime })).toBe(reversed)
  })

  it('签名时间由起始时刻与有效期派生', () => {
    expect(cosSignTime(1_700_000_000, 3600)).toBe('1700000000;1700003600')
  })
})

describe('对象键的编码', () => {
  it('参与签名的路径名是原样的键，只补开头一个斜杠', () => {
    // 与官方 SDK 同一条：键由服务端分配，本来就是 URL 安全的；这里不做百分号
    // 编码不是因为编码更安全，而是因为**签名要对应真正发出的那一行请求**。
    const key = 'galaxy/prj_abc/assets/image/ast_xyz'
    const { auth, signTime } = oracle(key, { host: HOST })
    expect(
      cosAuthorization({
        secretID: SECRET_ID,
        secretKey: SECRET_KEY,
        method: 'PUT',
        key: `/${key}`,
        headers: { host: HOST },
        signTime,
      }),
    ).toBe(auth)
  })
})
