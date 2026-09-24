import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import 'antd/dist/reset.css'

import { App } from './App'
import { tokenStorage } from './auth'
import { createTransport, setTransport } from './api/transport'

// 传输层在应用启动时初始化一次：它是全部出站请求的唯一入口，
// 凭证的读取方式也只在这里声明。
//
// baseUrl 留空表示同源：Connect handler 挂在过程名本身
// （/aladdin.<pkg>.<Service>/<Method>），没有统一前缀。
// 开发期由 vite 的 proxy 转发到后端，见 vite.config.ts。
setTransport(
  createTransport({
    baseUrl: '',
    getToken: () => tokenStorage.read(),
  }),
)

const container = document.getElementById('root')
if (container === null) {
  throw new Error('未找到挂载点 #root')
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
