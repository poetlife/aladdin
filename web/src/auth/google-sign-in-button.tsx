import { theme } from 'antd'
import { useEffect, useRef } from 'react'

import { useTheme } from '../theme'
import { mountGoogleButton } from './google-identity'

interface GoogleSignInButtonProps {
  /** 服务端下发的客户端标识。为空时调用方**不应**渲染本组件。 */
  clientId: string
  /** 拿到身份令牌的回调。令牌交给服务端，本组件不解读它。 */
  onCredential: (idToken: string) => void
  /**
   * 与所在处的 antd 按钮同档，缺省是 antd 的默认档（middle）。
   *
   * 这不是可选的美化项：GIS 的按钮自带 40px 高、14px 字，放在一排 32px 的按钮里
   * 会高出一截。而它只有 large/medium 两档可调，没有"跟着旁边"这一档，所以得由
   * 调用方按上下文告诉它——**没有"正确的一档"，只有"和邻居一致的一档"**。
   */
  size?: 'large' | 'middle'
}

/** antd 的档位名折算成 GIS 的：GIS 没有 middle，对应的是 medium；字号各自按站点取。 */
function googleSize(size: 'large' | 'middle'): { size: 'large' | 'medium'; fontSizeToken: 'fontSizeLG' | 'fontSize' } {
  return size === 'large'
    ? { size: 'large', fontSizeToken: 'fontSizeLG' }
    : { size: 'medium', fontSizeToken: 'fontSize' }
}

/**
 * Google 登录按钮。
 *
 * **是否渲染由调用方决定**，本组件不自己判断"该不该出现"——"未启用就不渲染
 * 入口"是登录页的职责（见 docs/design/identity/google-login.md），放在这里会
 * 让同一个判断出现两处。
 */
export function GoogleSignInButton({
  clientId,
  onCredential,
  size = 'middle',
}: GoogleSignInButtonProps): React.ReactNode {
  const holder = useRef<HTMLDivElement>(null)
  const { resolved } = useTheme()
  const { token } = theme.useToken()
  const { size: gisSize, fontSizeToken } = googleSize(size)
  const fontSize = token[fontSizeToken]

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

    // 卸载或重挂时先作废这次挂载：脚本加载是异步的，迟到的这一次若不作废，
    // 会和后一次各画一个按钮（开发期 StrictMode 每次挂载都会这样走一遍）。
    const controller = new AbortController()
    mountGoogleButton(element, {
      clientId,
      onCredential: (idToken) => {
        handler.current(idToken)
      },
      scheme: resolved,
      size: gisSize,
      // 圆角取全局那一个值（见 App.tsx）；字号跟本组件所在的档位走。
      borderRadius: token.borderRadius,
      fontSize,
      signal: controller.signal,
    }).catch(() => {
      // 加载失败时留空。登录页仍有其它方式，为一次第三方脚本的失败弹错
      // 只会让人以为整个登录坏了——而它其实只是少了一个入口。
    })

    return () => {
      controller.abort()
      // GIS 把按钮渲染进这个容器，重新挂载前要清掉，
      // 否则会叠出两个按钮。
      element.replaceChildren()
    }
    // GIS 的按钮配色、尺寸、圆角、字号都是渲染时定死的，一变只能重挂一次，
    // 不能靠样式覆盖（见 google-identity.ts）。
  }, [clientId, resolved, gisSize, token.borderRadius, fontSize])

  // 容器自己占满可用宽度并把按钮居中：GIS 只按固定像素宽度画按钮，
  // 容器若按内容撑宽，挂载时就量不到"还剩多少地方"，只能写死一个宽度，
  // 而写死的那个数在手机上必然过宽。
  return (
    <div
      ref={holder}
      data-testid="google-sign-in"
      style={{ width: '100%', display: 'flex', justifyContent: 'center' }}
    />
  )
}
