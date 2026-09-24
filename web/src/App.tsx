import { App as AntdApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { RouterProvider } from 'react-router-dom'

import { SessionProvider } from './auth'
import { router } from './router'

/**
 * 应用根组件。
 *
 * 层级顺序是有意义的：ConfigProvider 提供主题与本地化，
 * AntdApp 提供 message/notification 的上下文，
 * SessionProvider 提供会话与权限——页面在这三者之内。
 */
export function App(): React.ReactNode {
  return (
    <ConfigProvider locale={zhCN}>
      <AntdApp>
        <SessionProvider>
          <RouterProvider router={router} />
        </SessionProvider>
      </AntdApp>
    </ConfigProvider>
  )
}
