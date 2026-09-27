import { App as AntdApp, ConfigProvider, theme as antdTheme } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { useEffect } from 'react'
import { RouterProvider } from 'react-router-dom'

import { SessionProvider } from './auth'
import { router } from './router'
import { ThemeProvider, useTheme } from './theme'

/**
 * 应用根组件。
 *
 * 层级顺序是有意义的：ThemeProvider 先定下明暗（它要算给 ConfigProvider），
 * ConfigProvider 提供主题与本地化，AntdApp 提供 message/notification 的上下文，
 * SessionProvider 提供会话与权限——页面在这四者之内。
 */
export function App(): React.ReactNode {
  return (
    <ThemeProvider>
      <ThemedApp />
    </ThemeProvider>
  )
}

/**
 * 主题解析结果与 antd 之间的接线处。
 *
 * 明暗只换算法、不换 token：颜色全部由 antd 从同一份种子推导，
 * 界面各处再通过 `theme.useToken()` 取，避免出现第二套手写的色值。
 */
function ThemedApp(): React.ReactNode {
  const { resolved } = useTheme()

  return (
    <ConfigProvider
      locale={zhCN}
      theme={{
        algorithm: resolved === 'dark' ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
        token: { borderRadius: 8 },
      }}
    >
      <AntdApp>
        <PageBackground />
        <SessionProvider>
          <RouterProvider router={router} />
        </SessionProvider>
      </AntdApp>
    </ConfigProvider>
  )
}

/**
 * 把主题的页面底色刷到 body 上。
 *
 * 登录页、登录回调页与 403 页都渲染在外壳**之外**，没有 Layout 兜底；
 * 少了这一层，暗色主题下它们会是一块白底。
 */
function PageBackground(): null {
  const { token } = antdTheme.useToken()

  useEffect(() => {
    document.body.style.background = token.colorBgLayout
    document.body.style.color = token.colorText
  }, [token.colorBgLayout, token.colorText])

  return null
}
