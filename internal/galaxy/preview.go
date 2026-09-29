package galaxy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"
)

// 预览通道：草稿整组按发布的路径形状，从发布域上一条带**短时凭证**的地址上给出。
//
// 为什么是"给出地址"而不是"把字节塞进 iframe"：页面里的相对地址、站点绝对地址、
// 样式里的 `url()`、脚本里拼出来的路径，只有文档**有自己的地址**时才解析得了；
// 而一份 `srcdoc` 文档没有地址（它落在不透明源上）。发布态早就是这么做的——文本
// 由服务端在它的自然路径上给出，正是为了让文档里那些引用都成立（见
// docs/design/galaxy/site-model.md 的"取字节"）。预览与发布的差别只剩三处：取草稿
// 而不是取已发布的那一版、带凭证而不是公开匿名、资产指向私有区而不是公开区。

// PreviewPathSegment 是预览地址在 `/g/` 之后的固定分段。
//
// **它选一个不可能是工程标识的值**：工程标识的形状固定是 `prj_…`（见 NewProjectID），
// 因此 `p` 永远不会与某个工程自己的路径相撞——发布态与预览态因此可以共用 `/g/`
// 这一段前缀，而不需要一条"先判断是哪一类"的互斥规则。
const PreviewPathSegment = "p"

// PreviewGrantTTL 是一条预览凭证的有效时长。
//
// **它是一个常量，不是配置项。** 这类"凭据/通道的窗口"在本仓库一律留在代码里：
// 会话是 `DefaultSessionTTL`、资产与正文的短时地址是 `AssetURLTTL`、导航凭据是
// `githubStateTTL`，事件通道的寿命与心跳也各有一处常量（见
// docs/design/events/README.md）。配置只回答"这个部署长什么样"。**而这个键能
// 让谁获得什么**这一问的答案是"让拿到预览地址的人多看更久"——不是"什么也不能"，
// 因此按 AGENTS.md 第 7 条它不该存在（一旦存在，就一定会有一次把它调长的部署）。
//
// **取值是 5 分钟。** 凭证是纯 bearer（见 OpenPreview）：不认人、只认它自己，因此
// 窗口越短，泄露出去的价值越低。代价要说清：**页面开着超过 5 分钟后，页内跳转
// （`docs` 的导航、站内链接）会失效**——票过期了，而浏览器还在用地址里那张旧票；
// 「刷新」重新取一次即换新票。已经加载出来的图不受影响：资产的短时地址是 10 分钟，
// 比票长。
const PreviewGrantTTL = 5 * time.Minute

// ErrPreviewNotFound 是一次预览请求"取不到东西"的唯一否定结论。
//
// 凭证不存在、凭证过期、凭证不属于这个工程、工程不存在、路径不在草稿里、草稿还是
// 空的——**六者同一个结论**，与发布态那五者同一个道理：区分它们等于告诉一个猜地址
// 的人"这个工程的草稿是真的，只是过期了"。
var ErrPreviewNotFound = errors.New("预览地址不存在")

// ErrPreviewUnavailable 表示这个部署没有预览这条路（没有发布域或没有对象存储）。
var ErrPreviewUnavailable = errors.New("预览功能未启用")

// PreviewGrant 是一条预览凭证。
//
// 它**只做一件事**：把"这个工程的草稿可以按路径取"授权给任何拿到这个字符串的人，
// 直到失效时刻。它不承载别的能力——读不了别的工程，写不了，也发不了布。
type PreviewGrant struct {
	// Token 是凭证本体，出现在预览地址的路径里。
	Token string
	// ProjectID 是它授权的工程。
	ProjectID string
	// SubjectID 是签发时的主体，留痕用。
	SubjectID string
	// ExpiresAt 是失效时刻。**判定只比较它与现在**：一条没被清掉的行也不会多给
	// 一秒钟的访问权。
	ExpiresAt time.Time
	CreatedAt time.Time
}

// Expired 判定这条凭证在给定时刻是否已失效。
func (g PreviewGrant) Expired(now time.Time) bool { return !now.Before(g.ExpiresAt) }

// NewPreviewGrantToken 分配一个凭证本体（唯一入口）。
//
// 它是这条通道上唯一保密的那一段：不可猜（128 位随机），不带头缀，也不承载任何
// 可读信息。
func NewPreviewGrantToken() (string, error) { return newID("") }

// PreviewRoot 返回一个工程在预览态下的**站点根路径**，形如
// `/g/p/<凭证>/<工程标识>/`。
//
// 它有两个身份，两者必须一致：浏览器眼里它是页面的目录（相对地址按它解析），
// 交付那一步它是"发布根换成预览根"那次替换的目标。
func PreviewRoot(projectID, token string) string {
	return PublicPathPrefix + PreviewPathSegment + "/" + token + "/" + projectID + "/"
}

// SplitPreviewPath 把一条预览地址拆成（凭证，工程标识，条目路径）（唯一入口）。
//
// 第二个返回值为假表示这条地址不是一次预览请求。与 SplitSitePath 一样，**它是
// 形状解析，不是判定**：凭证真伪与路径是否存在由 OpenPreview 回答。
func SplitPreviewPath(requestPath string) (token, projectID, entryPath string, ok bool) {
	prefix := PublicPathPrefix + PreviewPathSegment + "/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(requestPath, prefix)
	token, rest, ok = cutSegment(rest)
	if !ok {
		return "", "", "", false
	}
	projectID, entryPath, ok = cutSegment(rest)
	if !ok {
		return "", "", "", false
	}
	return token, projectID, entryPath, true
}

// cutSegment 切下一段（到第一个 `/` 为止），并返回余下的部分。
//
// 只被上面的形状解析用：头部为空的那一段不是一段（`//` 与结尾的 `/` 都不接受），
// 因此"多写一个斜杠"这种地址不会落进任何一条预览路径。
func cutSegment(rest string) (head, tail string, ok bool) {
	if rest == "" {
		return "", "", false
	}
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		head, tail = rest[:slash], rest[slash+1:]
	} else {
		head, tail = rest, ""
	}
	if head == "" {
		return "", "", false
	}
	return head, tail, true
}

// PreviewTarget 是一次预览请求要交付的东西。
//
// 两种取字节方式与发布态逐条对应（见 server 那条约浏览器直连入口的说明）：文本由
// 服务端在自然路径上给出，资产给一个短时地址由浏览器直连。**这里没有第三种形状**
// ——预览不是一条"把站点拼成一份"的通道。
type PreviewTarget struct {
	// RedirectURL 非空表示这一条是资产：交给浏览器一个短时地址，字节不经服务端。
	RedirectURL string
	// ContentType 是文本条目的下发类型（按扩展名派生，见 ArtifactContentType）。
	ContentType string
	// Data 是文本条目的字节。
	Data []byte
}

// OpenPreview 解析一次预览请求要交付的东西（唯一入口）。
//
// **它没有"调用者"这个参数**：凭证就是授权。签发那一刻已经问过"这个人能不能读这个
// 工程"（见 PreviewDraft），此后这条地址自己成立——与资产那套短时地址同源。
func (s *Service) OpenPreview(ctx context.Context, token, projectID, requestPath string) (PreviewTarget, error) {
	if !s.previewEnabled() {
		return PreviewTarget{}, ErrPreviewNotFound
	}
	grant, err := s.store.GetPreviewGrant(ctx, token)
	if err != nil {
		if errors.Is(err, ErrPreviewGrantNotFound) {
			return PreviewTarget{}, ErrPreviewNotFound
		}
		return PreviewTarget{}, err
	}
	// 凭证绑定的工程与地址里的工程标识必须一致：地址是给浏览器解析的，凭证是授权
	// ——两者不一致说明有人在改地址，那就是一次否定结论。
	if grant.ProjectID != projectID || grant.Expired(s.now()) {
		return PreviewTarget{}, ErrPreviewNotFound
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectNotFound) {
			return PreviewTarget{}, ErrPreviewNotFound
		}
		return PreviewTarget{}, err
	}
	draft, err := s.store.GetDraft(ctx, projectID)
	if errors.Is(err, ErrDraftNotFound) {
		return PreviewTarget{}, ErrPreviewNotFound
	} else if err != nil {
		return PreviewTarget{}, err
	}
	if len(draft.Manifest) == 0 {
		return PreviewTarget{}, ErrPreviewNotFound
	}

	artifactPath := requestPath
	if artifactPath == "" {
		artifactPath = ArtifactPath(project.Form, project.Form.EntryPath())
	}
	return s.previewTarget(ctx, project, token, draft.Manifest, artifactPath)
}

// previewTarget 按形态把一条产物路径解析成要交付的东西。
//
// 两种形态的差别在这里只有一处：`static` 的产物就是文件组里的那一份（读它、替换
// 记号即可），`docs` 的页面**不存在于文件组里**——它由渲染产生，因此要把整组渲染
// 一遍才谈得上"这一页长什么样"。站点文件两种形态一样：按原路径给出。
func (s *Service) previewTarget(ctx context.Context, project Project, token string, manifest Manifest, artifactPath string) (PreviewTarget, error) {
	if project.Form != SiteFormDocs {
		entry, found := manifest.Find(artifactPath)
		if !found {
			return PreviewTarget{}, ErrPreviewNotFound
		}
		return s.previewTextEntry(ctx, project, token, manifest, entry, artifactPath)
	}

	// `docs`：站点文件（不是 markdown 的那些文本条目）按原路径给出。
	if entry, found := manifest.Find(artifactPath); found && !IsMarkdownPath(entry.Path) {
		return s.previewTextEntry(ctx, project, token, manifest, entry, artifactPath)
	}

	// 其余的是渲染出来的页面：整组渲染一次，取这一页。
	artifacts, err := s.buildPreviewArtifacts(ctx, project, manifest)
	if err != nil {
		return PreviewTarget{}, err
	}
	data, found := artifacts[artifactPath]
	if !found {
		return PreviewTarget{}, ErrPreviewNotFound
	}
	return PreviewTarget{
		ContentType: renderedContentType,
		Data:        rewritePreviewRoot(project.ID, token, s.origin, data),
	}, nil
}

// previewTextEntry 交付一条文本条目：资产重定向，文本读字节并逐字替换。
func (s *Service) previewTextEntry(ctx context.Context, project Project, token string, manifest Manifest, entry Entry, artifactPath string) (PreviewTarget, error) {
	if entry.Kind == EntryKindAsset {
		url, err := s.presignEntry(ctx, project.ID, entry)
		if err != nil {
			if errors.Is(err, ErrAssetNotFound) {
				return PreviewTarget{}, ErrPreviewNotFound
			}
			return PreviewTarget{}, err
		}
		if url == "" {
			return PreviewTarget{}, ErrPreviewNotFound
		}
		return PreviewTarget{RedirectURL: url}, nil
	}
	if s.assets == nil {
		return PreviewTarget{}, ErrAssetUnavailable
	}
	source, err := s.assets.Read(ctx, ContentObjectKey(project.ID, entry.Digest))
	if err != nil {
		return PreviewTarget{}, err
	}
	siteRoot := s.origin.SiteRoot(project.ID)
	substituted, err := SubstituteAssetMarkers(source, func(assetID string) (string, error) {
		asset, found := assetEntryByID(manifest, assetID)
		if !found {
			// 记号指不到条目是**内容的问题**，不是这次请求的问题：预览把这一处
			// 原样留着（与"预览不做裁剪"同源），由校验入口去指出它。
			return PlaceholderScheme + assetID, nil
		}
		return siteRoot + asset.Path, nil
	})
	if err != nil {
		return PreviewTarget{}, err
	}
	contentType, ok := ArtifactContentType(project.Form, artifactPath)
	if !ok {
		// 产物清单里的每一条都来自文件组，而文件组的路径都过了白名单；走到这里
		// 说明库里的数据不是这个工程写进去的。给一个中性类型而不是猜一个——猜错
		// 的表现是浏览器按 HTML 渲染一段别的字节。
		contentType = "application/octet-stream"
	}
	return PreviewTarget{
		ContentType: contentType,
		Data:        rewritePreviewRoot(project.ID, token, s.origin, substituted),
	}, nil
}

// rewritePreviewRoot 把文本里的**发布根**前缀逐字换成预览根。
//
// 这是预览唯一一处改写。构建产物写的是站点绝对地址（`/g/<标识>/assets/index-abc.js`），
// 它必须落在同一个工程的**草稿**上——留在发布根上会取到已发布的那一版，或者取到
// 一个不存在的位置。替换的是一段**已知字符串**（发布根本身），因此不解析、不枚举
// 引用位置，也就不会漏：出现在属性里、`url()` 里、脚本字符串里，都只是把这段文本
// 换掉（与 `asset://` 记号同一条性质）。
//
// 凭证放在路径里而不是查询串里，正是为了让相对地址也落回这条通道：浏览器按目录
// 解析相对地址时，会把 `<发布域>/g/p/<凭证>/<标识>/` 这一段原样带上。
func rewritePreviewRoot(projectID, token string, origin PublicOrigin, data []byte) []byte {
	if origin.IsZero() || token == "" {
		return data
	}
	return bytes.ReplaceAll(data, []byte(origin.SiteRoot(projectID)), []byte(PreviewRoot(projectID, token)))
}

// previewEnabled 是"预览这条路在不在"的唯一判据。
//
// 两个前提合起来才成立：内容字节所在的对象存储，以及**发布域**——预览通道落在发布
// 域上（见 docs/design/galaxy/site-model.md）。缺任一条时预览整体缺席，如实缺席，
// 而不是退回一份解析不了自己引用的文档。
func (s *Service) previewEnabled() bool { return s.assets != nil && !s.origin.IsZero() }

// PreviewDraft 给出草稿整站的预览入口地址。
//
// 它做两件事，顺序不可换：**先按权限与归属确认这个人能读这个工程**，再签发一条短时
// 凭证并把地址拼出来。凭证是这条地址唯一保密的那一段，因此它只能在这里产生。
//
// 返回空地址表示**草稿里还没有可预览的入口**（草稿为空、或连入口文件都没有）。那
// 不是错误，而是"还没内容"：界面该显示空态，而不是一个打不开的地址。
//
// entryPath 是要预览的那一份；为空表示入口。取不到时退回入口——前端手里的路径是它
// 上一次读到的清单，与此刻的草稿不一致时（命令行刚 push 过），退回入口比给出一个
// 必然 404 的地址好。
func (s *Service) PreviewDraft(ctx context.Context, subjectID, projectID, entryPath string) (string, error) {
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return "", err
	}
	if !s.previewEnabled() {
		return "", ErrPreviewUnavailable
	}
	draft, err := s.store.GetDraft(ctx, projectID)
	if errors.Is(err, ErrDraftNotFound) {
		return "", nil
	} else if err != nil {
		return "", err
	}

	// 入口存在与否看的是**源路径**（`index.html` / `index.md`），而地址指向的是
	// **产物路径**（`docs` 的入口是渲染出来的 `index.html`）。
	source := project.Form.EntryPath()
	if _, ok := draft.Manifest.Find(source); !ok {
		return "", nil
	}
	artifactPath := ArtifactPath(project.Form, source)
	if entryPath != "" {
		if _, ok := draft.Manifest.Find(entryPath); ok {
			artifactPath = ArtifactPath(project.Form, entryPath)
		}
	}

	token, err := NewPreviewGrantToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	grant := PreviewGrant{
		Token:     token,
		ProjectID: projectID,
		SubjectID: subjectID,
		ExpiresAt: now.Add(PreviewGrantTTL),
		CreatedAt: now,
	}
	// 写入时顺带清掉这个工程里已经过期的凭证（由存储在同一个事务里完成）：表的
	// 增长因此只由"还有多少条活着的凭证"决定，而预览请求是只读的。
	if err := s.store.PutPreviewGrant(ctx, grant, now); err != nil {
		return "", err
	}
	return s.previewURL(project, token, artifactPath), nil
}

// previewURL 拼出一条预览地址。**地址由服务端算好下发**，客户端不拼：客户端再拼
// 一份就是第二个来源（与 PageURL 同一条约定）。
func (s *Service) previewURL(project Project, token, entryPath string) string {
	if s.origin.IsZero() {
		return ""
	}
	return s.origin.PreviewBase() + PreviewRoot(project.ID, token) + entryPath
}
