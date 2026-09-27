package server

import "sync"

// subjectLifecycleGate 让"空主体认领"与"给主体授予角色"串行。
//
// 认领要读一条 RBAC 事实（这个主体有没有角色绑定）再移动身份。若两者之间
// 有人刚好给原主体授了角色，就会出现"身份移走了、角色还留在原主体"的搁浅。
// 这把锁把那条窗口关掉。角色授予是低频管理动作，一把全局锁足够；为它建一张
// 每主体锁表带来的复杂度不值。
//
// 它只在本进程内生效。当前部署是单实例，重定向流程本身也依赖进程内的一次性
// 状态；将来多副本时这条要与那些状态一起换成库级串行。
type subjectLifecycleGate struct {
	mu sync.Mutex
}

// lock 获取生命周期锁，返回释放函数。
func (g *subjectLifecycleGate) lock() func() {
	g.mu.Lock()
	return g.mu.Unlock
}
