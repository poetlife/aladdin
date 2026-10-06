package main

// 本文件的唯一职责：触发各服务 proto 描述符的注册。
//
// rbac.Resolve 通过 protoregistry.GlobalFiles 查方法描述符，而描述符只在
// 对应的 gen 包被 import 时注册。生成器不 import 任何服务实现，所以必须在
// 这里显式登记。
//
// **新增服务时必须在此追加一行**，否则该服务的方法在文档里会以
// "未找到服务描述符" 报错——这是刻意的：报错比静默产出一份缺扩展的文档好。
import (
	_ "github.com/poetlife/aladdin/api/gen/aladdin/events/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/objectstore/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/profile/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/skill/v1"
	_ "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
)
