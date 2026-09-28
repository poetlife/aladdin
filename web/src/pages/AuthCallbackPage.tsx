import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Alert, Card, Spin } from 'antd'

import { useSession } from '../auth'
import { isUnauthenticated, messageOf } from '../api/errors'
import * as identityApi from '../api/identity'

/** 回跳地址里三个 fragment 的键，与服务端 github_login_flow.go 中定义的取值一致。 */
const FRAGMENT_TOKEN = 'token'
const FRAGMENT_ERROR = 'error'
const FRAGMENT_BINDING = 'binding'

/** 服务端在失败时附上的固定标记。两者都不区分失败发生在哪一步，前端也只给通用提示。 */
const GITHUB_LOGIN_FAILED = 'github_login_failed'
const GITHUB_BIND_FAILED = 'github_bind_failed'

/**
 * 重定向型登录渠道的回调页。
 *
 * 服务端在渠道侧完成校验之后，把浏览器送回这里。它有两种去向：
 *
 *   - 登录：会话凭证放在地址的 **fragment** 里，交给会话层；
 *   - 绑定：地址里只有一个 `binding=<source>` 标记，待绑定凭据在 HttpOnly
 *     cookie 里，本页用**当前会话**兑换它——目标主体不由页面指定。
 *
 * 本页不参与任何判定——凭证的可信度完全由服务端决定。
 */
export function AuthCallbackPage(): React.ReactNode {
  const { adoptSessionToken } = useSession()
  const navigate = useNavigate()
  const [error, setError] = useState<string | null>(null)

  // StrictMode 会让挂载期的 effect 跑两次。这份凭据只存在于地址里，第一次
  // 读走并抹掉之后第二次就没有了，因此必须只处理一次。
  const started = useRef(false)

  useEffect(() => {
    if (started.current) {
      return
    }
    started.current = true

    const params = new URLSearchParams(globalThis.location.hash.replace(/^#/, ''))
    // 取出即抹掉：fragment 里装着会话凭证或一次绑定标记，没有理由让它留在
    // 地址栏与浏览器历史里。用 replaceState 而不是 pushState——回调这一步
    // 不该在后退历史里留一条记录。查询串保留：它与凭证无关。
    if (globalThis.location.hash !== '') {
      globalThis.history.replaceState(
        null,
        '',
        globalThis.location.pathname + globalThis.location.search,
      )
    }

    const token = params.get(FRAGMENT_TOKEN)
    if (token !== null && token !== '') {
      void adoptSessionToken(token)
        .then(() => {
          void navigate('/', { replace: true })
        })
        .catch((err: unknown) => {
          setError(messageOf(err))
        })
      return
    }

    const binding = params.get(FRAGMENT_BINDING)
    if (binding !== null && binding !== '') {
      void identityApi
        .completeIdentityBinding(binding)
        .then((resp) => {
          void navigate('/profile', {
            replace: true,
            state: { identityBound: binding, reclaimed: resp.reclaimed },
          })
        })
        .catch((err: unknown) => {
          // 绑定要求已认证：会话在跳转期间失效时说清楚，而不是甩一句
          // "未认证"让用户以为绑定本身坏了。
          setError(isUnauthenticated(err) ? '登录状态已失效，请重新登录后再试。' : messageOf(err))
        })
      return
    }

    if (params.get(FRAGMENT_ERROR) === GITHUB_LOGIN_FAILED) {
      setError('登录未完成，请重试。')
      return
    }
    if (params.get(FRAGMENT_ERROR) === GITHUB_BIND_FAILED) {
      setError('绑定未完成，请重试。')
      return
    }
    setError('这个地址不是登录回调，请从登录页重新开始。')
  }, [adoptSessionToken, navigate])

  if (error === null) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', padding: 'clamp(32px, 12vh, 96px) 16px 24px' }}>
        <Spin tip="正在完成…">
          <div style={{ width: '100%', maxWidth: 320, height: 80 }} />
        </Spin>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'center', padding: 'clamp(32px, 12vh, 96px) 16px 24px' }}>
      <Card style={{ width: '100%', maxWidth: 420, minWidth: 0 }}>
        <Alert type="error" message={error} style={{ marginBottom: 16 }} />
        <a href="/login">返回登录页</a>
      </Card>
    </div>
  )
}
