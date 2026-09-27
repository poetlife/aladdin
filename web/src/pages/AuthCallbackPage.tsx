import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Alert, Card, Spin } from 'antd'

import { useSession } from '../auth'
import { messageOf } from '../api/errors'

/** 回跳地址里两个 fragment 的键，与服务端 github_login_flow.go 中定义的取值一致。 */
const FRAGMENT_TOKEN = 'token'
const FRAGMENT_ERROR = 'error'

/** 服务端在失败时附上的标记。它不区分失败发生在哪一步，前端也只给一句通用提示。 */
const GITHUB_LOGIN_FAILED = 'github_login_failed'

/**
 * 重定向型登录渠道的回调页。
 *
 * 服务端在渠道侧完成校验之后，把浏览器送回这里，会话凭证放在地址的
 * **fragment** 里（fragment 不发往服务端，因此不进访问日志、不进 Referer）。
 *
 * 本页只做三件事：取出凭证、**立刻把它从地址栏抹掉**、交给会话层。它不参与
 * 任何判定——凭证的可信度完全由服务端决定。
 */
export function AuthCallbackPage(): React.ReactNode {
  const { adoptSessionToken } = useSession()
  const navigate = useNavigate()
  const [error, setError] = useState<string | null>(null)

  // StrictMode 会让挂载期的 effect 跑两次。这份凭证只存在于地址里，第一次
  // 读走并抹掉之后第二次就没有了，因此必须只处理一次。
  const started = useRef(false)

  useEffect(() => {
    if (started.current) {
      return
    }
    started.current = true

    const params = new URLSearchParams(globalThis.location.hash.replace(/^#/, ''))
    // 取出即抹掉：fragment 里装着会话凭证，没有理由让它留在地址栏与浏览器
    // 历史里。用 replaceState 而不是 pushState——回调这一步不该在后退历史里
    // 留一条记录。查询串保留：它与凭证无关，丢掉它只会制造一个难查的缺陷。
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
    if (params.get(FRAGMENT_ERROR) === GITHUB_LOGIN_FAILED) {
      setError('登录未完成，请重试。')
      return
    }
    setError('这个地址不是登录回调，请从登录页重新开始。')
  }, [adoptSessionToken, navigate])

  if (error === null) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 96 }}>
        <Spin tip="正在完成登录…">
          <div style={{ width: 320, height: 80 }} />
        </Spin>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'center', paddingTop: 96 }}>
      <Card style={{ width: 420 }}>
        <Alert type="error" message={error} style={{ marginBottom: 16 }} />
        <a href="/login">返回登录页</a>
      </Card>
    </div>
  )
}
