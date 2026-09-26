# 管理员登录后被重定向到 /forbidden

**日期**：2026-09-27
**收敛到的端**：表象在前端（路由裁剪），根因在服务端的接口返回值

## 症状

按 [deploy.md](../../deploy.md) 的引导步骤，用配置里那个邮箱完成 Google 登录后，
浏览器落在 `/forbidden`；服务端日志里 `WhoAmI` 与 `GetSessionPermissions` 都是 200。

## 收敛过程

按 [registry 的按端检索提示](../registry.md)逐端排除，四步都排除了才落到返回值上：

1. **前端**：`/forbidden` 来自 [require-permission.tsx](../../../web/src/auth/require-permission.tsx)——
   "已认证，但权限码集合不含该路由的基础权限"。所以要看的是集合的**内容**，不是路由配置。
2. **服务端判定**：受权限控制的方法全部放行。判定路径认识通配，排除"绑定没建立"。
3. **引导配置**：日志里有 `warn 已按引导配置建立第一个管理员`；`role_bindings` 与
   `subjects` 两表内容也对（绑定落在主体标识上、`default_scope` 为全局）。
   这一步排的是 deploy.md 排障表里那一行"引导是否生效"。
4. **接口返回值**：`GetSessionPermissions` 返回 `["*"]`，只有一条。

## 根因

[internal/rbac/expansion.go](../../../internal/rbac/expansion.go) 的 `expand` 把角色声明的
权限码**原样**输出，通配没有被展开成权限目录里的具体权限码。系统管理员的整个权限集合
就是 `"*"` 这一个写法，于是下发给前端的集合就是 `["*"]`。

前端只做集合成员判断、不解释通配（见
[frontend-permissions.md](../../design/rbac/frontend-permissions.md)）——
前端重复实现通配必然与服务端分歧，所以它拿到 `"*"` 等于什么权限都没有，
`has("rbac.role.read")` 为假，路由级裁剪把它重定向到 `/forbidden`。

**这条症状的辨识特征是"接口全放行、界面一个入口都进不去"**：判定路径走 `Matches`，
通配本来就能匹配，因此问题只暴露在下发给前端的那份集合里。看到这个组合时直接去查
`GetSessionPermissions` 的返回值，不要继续怀疑绑定或引导。

## 验证

对 system.admin 调用 `EffectivePermissions`，返回值应等于权限目录全集：

- 单测 `TestEngineEffectivePermissionsExpandsWildcard`（另有一条覆盖末段通配 `rbac.*`）
- 端到端 `TestSessionPermissionsAreExpanded`（走真实连接，断言前端实际收到的集合）
