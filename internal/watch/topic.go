package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/poetlife/aladdin/internal/rbac"
)

// 主题的形状：**一个类型名，一道斜杠，然后是资源标识**。
//
// 类型名由属主模块定义（`galaxy.project`、将来的 `identity.device`…），资源标识
// 是属主自己的那个不可猜标识。通道只按第一个斜杠切分，不解释任何一段。
const topicSeparator = "/"

// ErrUnknownTopicKind 表示订了一个没有注册过的主题类型（或主题形状不合法）。
//
// 它与"没有权限"是两件事：这一条是**装配缺陷或调用方拼错了**，改权限不会让它
// 成立。
var ErrUnknownTopicKind = errors.New("未注册的主题类型")

// Topic 拼出一个主题（唯一入口）。
//
// 形状只在这里拼一次：发布方与订阅方各拼一遍，就会出现"发布用一个分隔符、订阅用
// 另一个"这种只在运行时暴露的分叉。
func Topic(kind, id string) string { return kind + topicSeparator + id }

// Kind 是一个可订阅资源类型在通道这一侧的注册项。
//
// 它由**属主模块**提供：只有属主知道订阅它需要哪个权限码、以及"这个主体能不能看
// 这个具体资源"。通道不认识任何具体业务。
type Kind struct {
	// Permission 是订阅这类主题所需的权限码（取自权限目录，不得手写字符串）。
	//
	// 判定的唯一实现仍然是 rbac.Engine.Check——通道把它交给引擎，不自己判断。
	Permission rbac.PermissionCode
	// Authorize 判定这个主体能不能订阅这个具体资源。
	//
	// **它返回的应当是能直接发给调用方的错误**（属主用自己的错误映射，通道不认识
	// 任何领域的错误词表）。**"不是他的"与"不存在"必须返回同一个结论**——属主
	// 用自己的读取入口实现这一点，订阅因此不是一条工程枚举通道。
	Authorize func(ctx context.Context, subject rbac.Subject, id string) error
}

// Registry 是主题类型的注册表。
type Registry struct {
	mu    sync.RWMutex
	kinds map[string]Kind
}

// NewRegistry 构造一张空的注册表。
func NewRegistry() *Registry {
	return &Registry{kinds: make(map[string]Kind)}
}

// Register 注册一个主题类型。
//
// 装配期的错误（重复注册、权限码非法、判定缺失）**当场 panic**：它们只可能来自
// 写错的接线，而"启动就炸"比"某一次订阅失败"早得多、也清楚得多。
func (r *Registry) Register(kind string, entry Kind) {
	if kind == "" || strings.Contains(kind, topicSeparator) {
		panic(fmt.Sprintf("主题类型 %q 不合法：不能为空、也不能含 %q", kind, topicSeparator))
	}
	if !entry.Permission.Valid() {
		panic(fmt.Sprintf("主题类型 %q 的权限码 %q 形状非法", kind, entry.Permission))
	}
	if entry.Authorize == nil {
		panic(fmt.Sprintf("主题类型 %q 没有归属判定", kind))
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.kinds[kind]; exists {
		panic(fmt.Sprintf("主题类型 %q 被注册了两次", kind))
	}
	r.kinds[kind] = entry
}

// Resolve 把一个主题拆成它的注册项与资源标识。
//
// 切分取**第一个**分隔符：资源标识里再出现斜杠也不影响类型名的判定（将来的层级
// 标识因此不必改这里）。
func (r *Registry) Resolve(topic string) (Kind, string, error) {
	kind, id, found := strings.Cut(topic, topicSeparator)
	if !found || kind == "" || id == "" {
		return Kind{}, "", fmt.Errorf("%w: %q", ErrUnknownTopicKind, topic)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.kinds[kind]
	if !ok {
		return Kind{}, "", fmt.Errorf("%w: %q", ErrUnknownTopicKind, topic)
	}
	return entry, id, nil
}
