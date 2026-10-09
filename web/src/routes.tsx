import { Navigate } from 'react-router-dom'
import type { RouteObject } from 'react-router-dom'

import { RequirePermission } from './auth'
import { AppLayout } from './layouts/AppLayout'
import { PublicDocsLayout } from './layouts/PublicDocsLayout'
import { AuthCallbackPage } from './pages/AuthCallbackPage'
import { DeviceApprovalPage } from './pages/DeviceApprovalPage'
import { ForbiddenPage } from './pages/ForbiddenPage'
import { HomePage } from './pages/HomePage'
import { LoginPage } from './pages/LoginPage'
import { ProfilePage } from './pages/ProfilePage'
import { PermissionCatalogPage } from './pages/PermissionCatalogPage'
import { DeploymentPage } from './pages/admin/DeploymentPage'
import { TelemetryPage } from './pages/admin/TelemetryPage'
import { RolesPage } from './pages/RolesPage'
import { ScopesPage } from './pages/ScopesPage'
import { SubjectBindingsPage } from './pages/SubjectBindingsPage'
import { PermissionCodes } from './gen/permission-codes'
import { ChapterPage } from './pages/docs/ChapterPage'
import { CHAPTERS } from './pages/docs/chapters/manifest'
import { DocsIndexPage } from './pages/docs/DocsIndexPage'
import { ProjectEditorPage } from './pages/galaxy/ProjectEditorPage'
import { ProjectListPage } from './pages/galaxy/ProjectListPage'
import { SkillCatalogPage } from './pages/skill/SkillCatalogPage'
import { SkillDetailPage } from './pages/skill/SkillDetailPage'
import { PreviewPage } from './pages/galaxy/PreviewPage'
import { PublishedPage } from './pages/galaxy/PublishedPage'

/**
 * 路由表。
 *
 * 准入分成**三层**，不能合并：
 *
 *   - **公开**：站内文档区与一条分享地址。前者要给还没登录的人看（"先装命令行
 *     才能登录、登录了才看得到怎么装命令行"这个环得闭上），后者本来就是发给
 *     访客的。它们各有自己的外壳，且都**不**经过 `RequirePermission`。
 *   - 外壳（含首页与个人资料）只要**已认证**。零权限的主体也必须进得来——
 *     他拿到会话是成功的，只是什么都做不了、界面是空的；而"给自己起个名字"
 *     恰恰是他最需要做、也唯一能做的事（见 docs/design/profile/README.md）。
 *   - 具体的管理页面各自声明**基础权限**，那是页面准入条件的唯一声明位置，
 *     不散落在页面内部。
 *
 * 合成一层会让"零权限"与"无权访问某个管理页"变成同一个结论，而它们该有不同
 * 的走向：前者该看到一个空界面，后者该被明确告知无权访问。
 *
 * 本文件只有**路由表**，`createBrowserRouter` 在 router.tsx。分成两份是因为
 * 预渲染要在 Node 里按同一张表建一棵内存路由——在那里 import 一个已经建好的
 * 浏览器路由会直接抛错（Node 里没有 `window.history`）。
 */
export const routes: RouteObject[] = [
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
  // 站内文档区。它是**公开**的：准入只有路由这一处声明，未认证也能读
  //（见 docs/design/web/docs-area.md 的"放出去"）。
  //
  // 它因此**不在 `AppLayout` 之下**——那是"已认证"的外壳，带着账号区与管理范围，
  // 而访客没有"我是谁、我在哪个范围下工作"这两样东西。放出去给它的是自己的一层
  // 外壳，不是把已有那层放宽：`RequirePermission` 与 `AppLayout` 是父子，只把它从
  // 前者挪出来会让它连外壳一起丢掉（见 PublicDocsLayout.tsx）。
  //
  // 章节清单**不在这里**：它由 chapters/manifest.tsx 派生，与索引页、送给 agent 的
  // `.md`、`llms.txt` 是同一份。加一章只改那一处。
  {
    path: '/docs',
    element: <PublicDocsLayout />,
    children: [
      { index: true, element: <DocsIndexPage /> },
      ...CHAPTERS.map((chapter) => ({
        path: chapter.slug,
        element: <ChapterPage chapter={chapter} />,
      })),
      // 未知的章节回到索引。**这一条不能省**：少了它，`/docs/<没有这一章>` 会匹配上
      // 这一层的父路由、却没有子路由可渲染，于是公开外壳里空出一块——看起来像这一页
      // 加载坏了，而不是"没有这一章"。顶上那条 `*` 接不到它。
      { path: '*', element: <Navigate to="/docs" replace /> },
    ],
  },
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
          // 运维面。眼下是遥测与部署信息两页，独立成一个顶层入口而不是塞进 /access：
          // 那一组是**权限管理**（角色、授权、范围），这里放的是运行观测——"它在干什么"
          // 与"它跑的是哪一版"，两件事都不改任何东西。
          // 将来 #36 的访客统计并入同一页时，沿用这个入口。
          //
          // 两页各挂自己的权限码，不合成一层：看遥测与看版本号是两道门
          // （见 api/permissions/catalog.yaml 里两条码各自的说明）。
          {
            element: <RequirePermission require={[PermissionCodes.TelemetryRead]} />,
            children: [{ path: '/admin/telemetry', element: <TelemetryPage /> }],
          },
          {
            element: <RequirePermission require={[PermissionCodes.OpsDeploymentRead]} />,
            children: [{ path: '/admin/deployment', element: <DeploymentPage /> }],
          },
          // 平台技能目录。基础权限是 `skill.catalog.read`——目录与详情同属一块
          // 能力；纳管、同步、回滚与删除在页面内部按 `skill.catalog.write` 裁剪
          // （见 docs/design/skill/README.md）。
          {
            element: <RequirePermission require={[PermissionCodes.SkillCatalogRead]} />,
            children: [
              { path: '/skills', element: <SkillCatalogPage /> },
              { path: '/skills/:skillId', element: <SkillDetailPage /> },
            ],
          },
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
]
