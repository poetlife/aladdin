package server

// 本文件保留 Connect 相关的装配说明。
//
// 具体的 handler 注册在 server.go 的 New 中完成——只有一处，不拆两个文件，
// 否则"服务挂在哪里"这件事会有两个答案。
//
// 路径约定：Connect handler 挂在过程名本身（如
// /aladdin.rbac.v1.RBACService/ListRoles），**不加 /api 之类的统一前缀**。
// 这样 authMiddleware 里的 interceptor.Resolve(r.URL.Path) 可以直接拿到
// 过程名，不需要先剥前缀——少一处两侧必须对齐的魔法字符串。
//
// 前端相应的 baseUrl 为空（同源），vite 开发代理按 /aladdin. 前缀转发，
// 见 web/vite.config.ts。
