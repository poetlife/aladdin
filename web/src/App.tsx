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
        // 圆角全局只留一个值：antd 默认会按尺寸派生出 LG/SM 等档位（大号按钮因此
        // 比基础控件更圆），登录页的 Google 按钮还带着自己的 4px，三者在同一张卡片
        // 上就能看出差别。把会出现在界面上的那几档钉成同一个数，差异就不再来自"哪
        // 个组件"，而是由这里唯一决定。
        //
        // XS 不动：它管的是组件内部的小细节（如标签上的关闭角），不是用户认得的"面"。
        token: { borderRadius: 8, borderRadiusLG: 8, borderRadiusSM: 8 },
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
