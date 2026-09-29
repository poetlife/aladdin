package galaxy

import "github.com/poetlife/aladdin/internal/watch"

// 本文件是**工程状态变化通知**的领域侧唯一入口：主题的名字，以及往它上面发事件
// 的那一处调用（见 docs/design/events/README.md）。
//
// 事件在领域层发出，而不是在 RPC 层：写路径在领域层（那是它们的唯一入口），
// 放在 RPC 层意味着第二个调用方会静默漏发——而漏发一个写入点的表现形式，正是
// 这个模块要修的那个 bug（"别处改完，这一页不动"）。
//
// **事件不含内容，也不含"变了什么"**：它只说"这个工程的某处变了"。收到它的人
// 自己去读现状，因此事件的大小与工程的内容体积无关，也不会过期。

// ProjectTopicKind 是"一个工程"这个可订阅主题的类型名。
//
// 它被两处引用，两处用的是**同一个字面量**：写路径发事件时用它拼主题，装配处
// （internal/server）用它注册主题类型。注册项里的权限码与归属判定也在那处给出
// ——本模块只提供"这个资源可订阅、它的主题长这样"。
const ProjectTopicKind = "galaxy.project"

// publish 报告这个工程的某处变了。
//
// 它必须在写入**成功之后**调用：写失败（校验拒绝、存储故障）不产生事件，否则
// 订阅者会为一次什么都没发生的操作白重拉一遍。
//
// 没有总线时（测试、或不需要推送的部署）是空操作。
func (s *Service) publish(projectID string) {
	if s.events == nil {
		return
	}
	s.events.Publish(ProjectTopic(projectID))
}

// publishDeleted 报告工程已删除，并让这个主题退场。
//
// 顺序是刻意的：先发一条普通的"变了"、再让它退场。订阅者收到事件后去读一次，
// 拿到"工程不存在"，页面于是显示那句既有的话；此后这条主题不会再有任何事件
// （资源没了，也没有发布方），留在订阅集合里只是让它挂在一条永远不会到来的
// 事件上。
func (s *Service) publishDeleted(projectID string) {
	if s.events == nil {
		return
	}
	s.events.Publish(ProjectTopic(projectID))
	s.events.Close(ProjectTopic(projectID))
}

// ProjectTopic 给出一个工程的订阅主题（唯一入口）。
//
// 订阅方（工作台）与发布方都调它，因此主题的形状只有一处定义——各拼一遍会出现
// "发布用一个分隔符、订阅用另一个"这种只在运行时暴露的分叉。
func ProjectTopic(projectID string) string {
	return watch.Topic(ProjectTopicKind, projectID)
}
