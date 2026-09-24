import { Button, Result } from 'antd'
import { useNavigate } from 'react-router-dom'

/**
 * 无权限页。
 *
 * 刻意做成一个独立页面而非静默跳回首页：静默跳转会让用户以为点击失灵，
 * 从而反复尝试而不是去申请授权。
 */
export function ForbiddenPage(): React.ReactNode {
  const navigate = useNavigate()
  return (
    <Result
      status="403"
      title="无权限"
      subTitle="当前作用域下你没有访问该页面所需的权限。若认为这是误判，请联系管理员核对角色授权。"
      extra={
        <Button type="primary" onClick={() => void navigate('/')}>
          返回首页
        </Button>
      }
    />
  )
}
