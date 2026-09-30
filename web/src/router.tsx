import { createBrowserRouter, Navigate } from 'react-router-dom'

import { RequirePermission } from './auth'
import { AppLayout } from './layouts/AppLayout'
import { AuthCallbackPage } from './pages/AuthCallbackPage'
import { DeviceApprovalPage } from './pages/DeviceApprovalPage'
import { ForbiddenPage } from './pages/ForbiddenPage'
import { HomePage } from './pages/HomePage'
import { LoginPage } from './pages/LoginPage'
import { ProfilePage } from './pages/ProfilePage'
import { PermissionCatalogPage } from './pages/PermissionCatalogPage'
import { RolesPage } from './pages/RolesPage'
import { ScopesPage } from './pages/ScopesPage'
import { SubjectBindingsPage } from './pages/SubjectBindingsPage'
import { PermissionCodes } from './gen/permission-codes'
import { CliPage } from './pages/docs/CliPage'
import { DocsIndexPage } from './pages/docs/DocsIndexPage'
import { GalaxyPage } from './pages/docs/GalaxyPage'
import { ProjectEditorPage } from './pages/galaxy/ProjectEditorPage'
import { ProjectListPage } from './pages/galaxy/ProjectListPage'
import { PreviewPage } from './pages/galaxy/PreviewPage'
import { PublishedPage } from './pages/galaxy/PublishedPage'

/**
 * 路由表。
 *
 * 准入分成**两层**，不能合成一层：
 *
 *   - 外壳（含首页与个人资料）只要**已认证**。零权限的主体也必须进得来——
 *     他拿到会话是成功的，只是什么都做不了、界面是空的；而"给自己起个名字"
 *     恰恰是他最需要做、也唯一能做的事（见 docs/design/profile/README.md）。
 *   - 具体的管理页面各自声明**基础权限**，那是页面准入条件的唯一声明位置，
 *     不散落在页面内部。
 *
 * 合成一层会让"零权限"与"无权访问某个管理页"变成同一个结论，而它们该有不同
 * 的走向：前者该看到一个空界面，后者该被明确告知无权访问。
 */
export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  // 重定向型登录渠道的回调落点。它必须在 RequirePermission **之外**：
  // 浏览器刚跳回来时凭证还只在地址的 fragment 里，尚未落盘。
  //
  // 它**不在 /auth/ 下**：那一段被反向代理整段转发给服务端（服务端的
  // 回调端点在 /auth/github/），前端在这里放路由会被服务端接走。
  { path: '/login/callback', element: <AuthCallbackPage /> },
  { path: '/forbidden', element: <ForbiddenPage /> },
  // 主站壳：一条**已发布内容**的分享地址落点（`/g/<工程标识>[/docs][/路径]`）。
  //
  // **它必须在 RequirePermission 之外，也在 AppLayout 之外。** 访客打开一条分享
  // 地址时没有会话，而"分享给没登录的人"正是这条地址唯一的用途；它也不该套上创作
  // 面的外壳——那一块与访客无关。页面自己只做一件事：把这条路径解析成发布域上的
  // 地址，再交给跨源沙箱 iframe（见 docs/design/galaxy/publication.md 的"主站壳"）。
  //
  // 用通配段而不是把槽解析写在这里：`docs` 那一段的判定只有服务端一处。这条路由
  // 也不能落在 `path: '*'` 兜底之后，否则整条分享地址会被送去首页。
  { path: '/g/*', element: <PublishedPage /> },
  {
    element: <RequirePermission />,
    children: [
      {
        element: <AppLayout />,
        children: [
          { path: '/', element: <HomePage /> },
          { path: '/profile', element: <ProfilePage /> },
          // 命令行登录的批准页。它只要**已认证**：零权限的主体也要能批准
          // 自己的登录，因此不挂在任何权限码之下。
          //
          // 地址由服务端拼进对外地址交给终端（internal/server 的
          // DeviceApprovalPath），改这里的路径要同时改那里。
          { path: '/device', element: <DeviceApprovalPage /> },
          // 文档区。它与外壳同属一回事：**文档是前端的一部分**（路由 + 组件），
          // 不是另一个站点。将来"放出去"改变的是准入，不是换宿主
          //（见 docs/design/web/docs-area.md）。
          //
          // 准入只要**已认证**、不要权限码：零权限的主体最需要它——否则
          // "先装命令行才能登录、登录了才看得到怎么装命令行"这个环闭不上。
          { path: '/docs', element: <DocsIndexPage /> },
          { path: '/docs/cli', element: <CliPage /> },
          { path: '/docs/galaxy', element: <GalaxyPage /> },
          {
            element: <RequirePermission require={[PermissionCodes.RbacRoleRead]} />,
            children: [
              { path: '/access/roles', element: <RolesPage /> },
              // 权限码目录与角色定义同一道门：它要显示"哪些角色持有这个码"，
              // 那是角色信息（见 docs/design/rbac/management-ui.md）。
              { path: '/access/codes', element: <PermissionCatalogPage /> },
            ],
          },
          // 人员授权：按主体查询绑定、授予与回收。**没有"按范围列全部绑定"的
          // 接口**，所以这一页以主体标识为键（见 docs/design/rbac/management-ui.md）。
          {
            element: <RequirePermission require={[PermissionCodes.RbacSubjectRead]} />,
            children: [{ path: '/access/subjects', element: <SubjectBindingsPage /> }],
          },
          // 范围目录：这个部署里登记了哪些范围。绑定只能指向已登记的范围
          //（见 docs/design/rbac/scopes.md）。
          {
            element: <RequirePermission require={[PermissionCodes.RbacScopeRead]} />,
            children: [{ path: '/access/scopes', element: <ScopesPage /> }],
          },
          // 权限管理原先挂在 /roles 下。旧地址留一次跳转：部署文档里
          // 的 SPA 回落冒烟步骤正是拿它当例子（见 docs/deploy.md）。
          { path: '/roles', element: <Navigate to="/access/roles" replace /> },
          // galaxy 创作面。基础权限是 `galaxy.project.read`——列表与编辑器
          // 同属一块能力；编辑器内部的写/发布/资产入口再按各自权限码裁剪。
          {
            element: <RequirePermission require={[PermissionCodes.GalaxyProjectRead]} />,
            children: [
              { path: '/galaxy', element: <ProjectListPage /> },
              { path: '/galaxy/:projectId', element: <ProjectEditorPage /> },
              // 单独打开的预览。工作台里预览与源码共用一块面积、切换着看，因此
              // "改的时候看不见渲染结果"——这一页是那件事的出口，读的仍是草稿。
              // 它只要读权限：看一眼草稿不该要求能改它。
              { path: '/galaxy/:projectId/preview', element: <PreviewPage /> },
            ],
          },
        ],
      },
    ],
  },
  { path: '*', element: <Navigate to="/" replace /> },
])
