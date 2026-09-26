import { useEffect, useRef } from 'react'

import { mountGoogleButton } from './google-identity'

interface GoogleSignInButtonProps {
  /** 服务端下发的客户端标识。为空时调用方**不应**渲染本组件。 */
  clientId: string
  /** 拿到身份令牌的回调。令牌交给服务端，本组件不解读它。 */
  onCredential: (idToken: string) => void
}

/**
 * Google 登录按钮。
 *
 * **是否渲染由调用方决定**，本组件不自己判断"该不该出现"——"未启用就不渲染
 * 入口"是登录页的职责（见 docs/design/identity/google-login.md），放在这里会
 * 让同一个判断出现两处。
 */
export function GoogleSignInButton({ clientId, onCredential }: GoogleSignInButtonProps): React.ReactNode {
  const holder = useRef<HTMLDivElement>(null)

  // 回调放进 ref：GIS 的 initialize 只在挂载时调一次，而 React 每次渲染都会
  // 给出新的函数身份。不这样做，就得把 onCredential 写进依赖数组，
  // 于是每渲染一次都重新 initialize 一遍。
  const handler = useRef(onCredential)
  handler.current = onCredential

  useEffect(() => {
    const element = holder.current
    if (element === null) {
      return
    }

    mountGoogleButton(element, clientId, (idToken) => {
      handler.current(idToken)
    }).catch(() => {
      // 加载失败时留空。登录页仍有其它方式，为一次第三方脚本的失败弹错
      // 只会让人以为整个登录坏了——而它其实只是少了一个入口。
    })

    return () => {
      // GIS 把按钮渲染进这个容器，重新挂载前要清掉，
      // 否则会叠出两个按钮。
      element.replaceChildren()
    }
  }, [clientId])

  return <div ref={holder} data-testid="google-sign-in" />
}
