package skill

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/tagging"
)

// Service 是技能目录的全部行为。
type Service struct {
	store   Store
	objects objectstore.Store
	remote  Remote
	logger  *zap.Logger
	now     func() time.Time
}

// Deps 是 Service 的装配输入。
type Deps struct {
	// Store 是持久化入口。
	Store Store
	// Objects 是私有区对象的读写入口。**为 nil 表示这个部署没有配置对象存储**：
	// 技能目录整体不可用——它的字节没有地方放。这与 galaxy 的资产同一条降级
	// 取向（见 docs/design/skill/README.md）。
	Objects objectstore.Store
	// Remote 是远端拉取的出口。为 nil 表示装配缺失（生产上总是有）。
	Remote Remote
	// Logger 是留痕出口。为 nil 时不留痕（测试里常这样）。
	Logger *zap.Logger
	// Now 可注入，供测试控制时间。
	Now func() time.Time
}

// NewService 按依赖装配服务。
func NewService(deps Deps) *Service {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{
		store:   deps.Store,
		objects: deps.Objects,
		remote:  deps.Remote,
		logger:  logger,
		now:     now,
	}
}

// Capabilities 汇报部署形态的边界。
//
// 它是**能力下发点**（与 galaxy 的 Capabilities 同一取向）：能力由服务端说，
// 客户端不猜。前两项是部署形态的事实、不因调用者而异，后三项是包契约的上限——
// 界面据此说明"什么样的仓库纳得进来"，而不是等一次纳管失败才把上限讲给管理员听。
type Capabilities struct {
	CatalogEnabled  bool
	ImportEnabled   bool
	MaxFiles        int
	MaxFileBytes    int
	MaxPackageBytes int
}

// Capabilities 返回当前部署的边界。
func (s *Service) Capabilities() Capabilities {
	return Capabilities{
		CatalogEnabled:  s.objects != nil,
		ImportEnabled:   s.objects != nil && s.remote != nil,
		MaxFiles:        MaxFiles,
		MaxFileBytes:    MaxFileBytes,
		MaxPackageBytes: MaxPackageBytes,
	}
}

// View 是目录里一条技能**对某个主体**呈现的样子。
//
// 收藏与使用量都不是技能自身的属性（前者挂在主体上，后者是一段时间内的读数），
// 因此它们在这一层合成，不与 Skill 混在一起。
type View struct {
	Skill
	// Favorited 是调用者是否收藏了它。
	Favorited bool
	// Usage 是最近 30 天的使用量。**只有计数，没有"谁"**。
	Usage Usage
}

// ListResult 是一次列表的结论。
type ListResult struct {
	Skills []View
	// AvailableTags 是整个目录已有的标签（**不随当前筛选收窄**），供界面给出
	// 候选项——"这个部署里有哪些标签"没有第二个地方回答。
	AvailableTags []string
	// Truncated 表示因为条数上限而被截断。
	Truncated bool
}

// Filter 是一次列表的筛选条件。
type Filter struct {
	// Query 是关键词。大小写不敏感的子串匹配，匹配有效标题与有效简介。
	Query string
	// Tags 是标签筛选，多值**取交集**。取值会被归一化。
	Tags []string
	// FavoritedOnly 表示只看调用者收藏过的。
	FavoritedOnly bool
}

// ListSkills 列出 / 搜索目录（唯一入口）。
func (s *Service) ListSkills(ctx context.Context, subjectID string, filter Filter) (ListResult, error) {
	skills, err := s.store.ListSkills(ctx)
	if err != nil {
		return ListResult{}, err
	}
	// 候选标签取**筛选之前**的全集：随当前结果收窄的表现是"筛掉了某一条，它的
	// 标签就从候选里消失了"，于是再想筛回来已经点不到。
	available := availableTags(skills)

	wanted, err := normalizeFilterTags(filter.Tags)
	if err != nil {
		return ListResult{}, err
	}
	favorites, err := s.store.FavoriteIDs(ctx, subjectID)
	if err != nil {
		return ListResult{}, err
	}
	usage, err := s.usageFor(ctx, skills)
	if err != nil {
		return ListResult{}, err
	}

	matched := make([]View, 0, len(skills))
	for _, item := range skills {
		if !matchesFilter(item, filter.Query, wanted, filter.FavoritedOnly, favorites) {
			continue
		}
		matched = append(matched, View{
			Skill:     item,
			Favorited: favorites[item.ID],
			Usage:     usage[item.ID],
		})
	}
	sortViews(matched)

	truncated := len(matched) > ListLimit
	if truncated {
		matched = matched[:ListLimit]
	}
	return ListResult{Skills: matched, AvailableTags: available, Truncated: truncated}, nil
}

// GetSkill 读取一个技能对某个主体呈现的样子。
func (s *Service) GetSkill(ctx context.Context, subjectID, skillID string) (View, error) {
	item, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		return View{}, err
	}
	favorites, err := s.store.FavoriteIDs(ctx, subjectID)
	if err != nil {
		return View{}, err
	}
	usage, err := s.store.Usage(ctx, []string{skillID}, s.usageSince())
	if err != nil {
		return View{}, err
	}
	return View{Skill: item, Favorited: favorites[skillID], Usage: usage[skillID]}, nil
}

// FileContent 是一次取用的结果。
type FileContent struct {
	Path   string
	Digest string
	Data   []byte
}

// GetFile **取用**一条文件的字节（唯一入口）。
//
// 它是使用量的**计量点**，且只有一个。计数是**尽力而为**：写不进去也照常把正文
// 交出去——"统计不准"与"我拿不到技能"是两件事，前者不该让后者发生。
func (s *Service) GetFile(ctx context.Context, subjectID, skillID, filePath string) (FileContent, error) {
	if s.objects == nil {
		return FileContent{}, ErrObjectStoreUnavailable
	}
	item, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		return FileContent{}, err
	}
	file, found := findFile(item.Current.Files, filePath)
	if !found {
		return FileContent{}, fmt.Errorf("%w: %s 里没有 %q", ErrFileNotFound, skillID, filePath)
	}
	data, err := s.objects.Read(ctx, ContentObjectKey(file.Digest))
	if err != nil {
		return FileContent{}, mapObjectError(err)
	}

	if err := s.store.RecordUse(ctx, skillID, subjectID, s.now()); err != nil {
		s.logger.Warn("技能使用计数失败",
			zap.String("skill_id", skillID), zap.Error(err))
	}
	return FileContent{Path: file.Path, Digest: file.Digest, Data: data}, nil
}

// ListVersions 返回一个技能的版本（最新的在前）。
func (s *Service) ListVersions(ctx context.Context, skillID string) ([]Version, error) {
	if _, err := s.store.GetSkill(ctx, skillID); err != nil {
		return nil, err
	}
	return s.store.ListVersions(ctx, skillID)
}

// SetFavorite 设置或取消收藏。
func (s *Service) SetFavorite(ctx context.Context, subjectID, skillID string, favorited bool) error {
	if _, err := s.store.GetSkill(ctx, skillID); err != nil {
		return err
	}
	return s.store.SetFavorite(ctx, skillID, subjectID, favorited)
}

// ImportParams 是一次纳管的输入。
type ImportParams struct {
	// RepositoryURL 是远端仓库地址，只接受 github.com 的仓库根形状。
	RepositoryURL string
	// Ref 是分支 / 标签 / 提交标识。空表示仓库的默认分支。
	Ref string
	// SubPath 是包在仓库里的子路径。空表示仓库根。
	SubPath string
	// Title 与 Summary 是说明层的初值。留空表示走回退。
	Title   string
	Summary string
	// Tags 是初始标签。
	Tags []string
}

// Import 从远端纳管一个新技能（唯一入口）。
//
// 五步的顺序是**契约的一部分**（见 docs/design/skill/onboarding.md）：前四步不写
// 任何东西，第五步是一个整体。因此校验失败时库里没有新行、桶上也没有为它写的新
// 对象；而字节先于行落地的代价是"行写失败会留下无引用的对象"——那与"删除技能不
// 删字节"是同一种残留，不为此引入两阶段提交。
func (s *Service) Import(ctx context.Context, params ImportParams) (Skill, error) {
	if s.objects == nil {
		return Skill{}, ErrObjectStoreUnavailable
	}
	if s.remote == nil {
		return Skill{}, ErrRemoteNotConfigured
	}

	repo, ref, subPath, err := parseSource(params.RepositoryURL, params.Ref, params.SubPath)
	if err != nil {
		return Skill{}, err
	}
	title, summary, tags, err := NormalizeMetadata(params.Title, params.Summary, params.Tags)
	if err != nil {
		return Skill{}, err
	}

	commit, err := s.remote.ResolveCommit(ctx, repo, ref)
	if err != nil {
		return Skill{}, err
	}
	pkg, err := s.fetchPackage(ctx, repo, commit, subPath)
	if err != nil {
		return Skill{}, err
	}

	skillID, err := NewSkillID()
	if err != nil {
		return Skill{}, err
	}
	version, err := s.newVersion(skillID, commit, pkg)
	if err != nil {
		return Skill{}, err
	}
	if err := s.writeObjects(ctx, pkg); err != nil {
		return Skill{}, err
	}

	item := Skill{
		ID:      skillID,
		Title:   title,
		Summary: summary,
		Tags:    tags,
		Source: Source{
			Owner: repo.Owner, Name: repo.Name,
			Ref: ref, SubPath: subPath, Commit: commit,
		},
		Current: version,
	}
	if err := s.store.CreateSkill(ctx, item); err != nil {
		return Skill{}, err
	}
	s.logger.Info("技能纳管",
		zap.String("skill_id", skillID),
		zap.String("repository", repo.Owner+"/"+repo.Name),
		zap.String("commit", commit),
		zap.Int("files", len(version.Files)))
	return item, nil
}

// Resync 追远端：有新提交则产生新版本并切指针（唯一入口）。
//
// 第二个返回值表示远端是否有变化。**没有变化时不产生版本、不留痕、不动指针**，
// 否则"这个技能上次真的更新是什么时候"会变成一个看不出答案的问题。
//
// 失败时当前指针**不动**：校验没过、远端挂了都只是"这一次没同步成功"。
//
// **回滚之后再同步不会把指针挪回去**：它只在远端有新提交时动指针，而回滚不改变
// "已同步到哪个提交"。这是刻意的——一次习惯性的同步不该悄悄撤销一次刚做的回滚。
func (s *Service) Resync(ctx context.Context, skillID string) (Skill, bool, error) {
	if s.objects == nil {
		return Skill{}, false, ErrObjectStoreUnavailable
	}
	if s.remote == nil {
		return Skill{}, false, ErrRemoteNotConfigured
	}
	current, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		return Skill{}, false, err
	}
	// 来源从技能行读，**不接受调用方另给一个**：接口面上没有"改来源"这个方法，
	// 同步因此不可能把内容的来源换掉。
	repo, ref, subPath, err := parseSource(current.Source.URL(), current.Source.Ref, current.Source.SubPath)
	if err != nil {
		return Skill{}, false, err
	}
	commit, err := s.remote.ResolveCommit(ctx, repo, ref)
	if err != nil {
		return Skill{}, false, err
	}
	if commit == current.Source.Commit {
		return current, false, nil
	}
	pkg, err := s.fetchPackage(ctx, repo, commit, subPath)
	if err != nil {
		return Skill{}, false, err
	}
	version, err := s.newVersion(skillID, commit, pkg)
	if err != nil {
		return Skill{}, false, err
	}
	if err := s.writeObjects(ctx, pkg); err != nil {
		return Skill{}, false, err
	}
	source := current.Source
	source.Commit = commit
	if err := s.store.AppendVersion(ctx, skillID, version, source); err != nil {
		return Skill{}, false, err
	}
	s.logger.Info("技能同步",
		zap.String("skill_id", skillID),
		zap.String("commit", commit),
		zap.Int("files", len(version.Files)))
	updated, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		return Skill{}, false, err
	}
	return updated, true, nil
}

// SetCurrentVersion 把当前指针切到该技能已有的某个版本（回滚）。
func (s *Service) SetCurrentVersion(ctx context.Context, skillID, versionID string) (Skill, error) {
	if err := s.store.SetCurrentVersion(ctx, skillID, versionID); err != nil {
		return Skill{}, err
	}
	s.logger.Info("技能回滚",
		zap.String("skill_id", skillID), zap.String("version_id", versionID))
	return s.store.GetSkill(ctx, skillID)
}

// UpdateMetadata 改说明层（唯一入口）。
//
// **它不改内容层任何一项**：当前版本、文件清单与所有字节在改动前后逐字不变。
// 写入表达的是**期望的完整状态**：空串清空标题或简介（即回到回退值），标签以请求
// 集合整体替换。
func (s *Service) UpdateMetadata(ctx context.Context, skillID, title, summary string, tags []string) (Skill, error) {
	if _, err := s.store.GetSkill(ctx, skillID); err != nil {
		return Skill{}, err
	}
	title, summary, normalized, err := NormalizeMetadata(title, summary, tags)
	if err != nil {
		return Skill{}, err
	}
	if err := s.store.UpdateMetadata(ctx, skillID, title, summary, normalized); err != nil {
		return Skill{}, err
	}
	return s.store.GetSkill(ctx, skillID)
}

// Delete 删除一个技能（唯一入口）。
//
// 删的是**行**：技能、版本、标签、收藏与使用记录一起消失，而桶上的字节不删
// （见 package.go 的 ContentObjectKey）。
func (s *Service) Delete(ctx context.Context, skillID string) error {
	if _, err := s.store.GetSkill(ctx, skillID); err != nil {
		return err
	}
	if err := s.store.DeleteSkill(ctx, skillID); err != nil {
		return err
	}
	s.logger.Info("技能删除", zap.String("skill_id", skillID))
	return nil
}

// PurgeUsage 回收超过保留期的使用日次。在服务端启动路径上调用。
func (s *Service) PurgeUsage(ctx context.Context) error {
	return s.store.DeleteUsageBefore(ctx, s.now().Add(-UsageRetention))
}

// parseSource 校验收敛来源的三项。
//
// 三个入口各自只有一处实现（见 source.go），这里只是把它们凑成一次调用。
func parseSource(repositoryURL, ref, subPath string) (Repository, string, string, error) {
	repo, err := ParseRepositoryURL(repositoryURL)
	if err != nil {
		return Repository{}, "", "", err
	}
	parsedRef, err := ParseRef(ref)
	if err != nil {
		return Repository{}, "", "", err
	}
	parsedSubPath, err := ParseSubPath(subPath)
	if err != nil {
		return Repository{}, "", "", err
	}
	return repo, parsedRef, parsedSubPath, nil
}

// fetchPackage 取回并校验一棵树。**它不写任何东西**——写上一步在校验通过之后。
func (s *Service) fetchPackage(ctx context.Context, repo Repository, commit, subPath string) (Package, error) {
	tree, err := s.remote.FetchTree(ctx, repo, commit, subPath)
	if err != nil {
		return Package{}, err
	}
	return BuildPackage(tree)
}

// newVersion 从一份已校验的包造出一个版本。
func (s *Service) newVersion(skillID, commit string, pkg Package) (Version, error) {
	versionID, err := newVersionID()
	if err != nil {
		return Version{}, err
	}
	files := make([]File, 0, len(pkg.Files))
	for _, file := range pkg.Files {
		files = append(files, File{
			Path:      file.Path,
			Digest:    file.Digest,
			SizeBytes: file.SizeBytes(),
		})
	}
	return Version{
		ID:          versionID,
		SkillID:     skillID,
		Commit:      commit,
		Name:        pkg.Manifest.Name,
		Description: pkg.Manifest.Description,
		Files:       files,
		CreatedAt:   s.now(),
	}, nil
}

// writeObjects 把一份包里的字节写进私有区（按内容摘要，仅当不存在时写入）。
//
// **它落在行之前**：字节先写、行后写，因此库里出现的每一个摘要都一定已经有一份
// 字节在桶上。反过来（先写行再写字节）会让一段不可见的窗口里存在"清单指着不存在
// 的对象"，而那个状态一旦被读到就是一次取用失败。
func (s *Service) writeObjects(ctx context.Context, pkg Package) error {
	for _, file := range pkg.Files {
		if _, err := objectstore.PutContentObject(
			ctx, s.objects, ContentObjectKey(file.Digest), file.Digest, file.Data); err != nil {
			if errors.Is(err, objectstore.ErrDigestMismatch) {
				// 摘要是我们自己算的，走到这里说明有一处实现不一致——它是缺陷，
				// 不是内容问题，因此不折成 ErrPackageInvalid。
				return fmt.Errorf("技能内容摘要不自洽（路径 %q）: %w", file.Path, err)
			}
			return mapObjectError(err)
		}
	}
	return nil
}

// usageFor 读出一批技能在统计窗口内的使用量。
func (s *Service) usageFor(ctx context.Context, skills []Skill) (map[string]Usage, error) {
	ids := make([]string, 0, len(skills))
	for _, item := range skills {
		ids = append(ids, item.ID)
	}
	return s.store.Usage(ctx, ids, s.usageSince())
}

// usageSince 返回统计窗口的起点。
func (s *Service) usageSince() time.Time { return s.now().Add(-UsageWindow) }

// availableTags 返回整个目录已有的标签（去重并升序）。
func availableTags(skills []Skill) []string {
	seen := map[string]bool{}
	for _, item := range skills {
		for _, tag := range item.Tags {
			seen[tag] = true
		}
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

// normalizeFilterTags 归一化筛选用的标签。
//
// 与写入时走的是**同一处**归一化：筛选用另一种写法的表现是"这个标签看着对、就是
// 筛不出来"。
func normalizeFilterTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	return tagging.Normalize(tags)
}

// matchesFilter 判定一条技能是否命中筛选。
func matchesFilter(item Skill, query string, tags []string, favoritedOnly bool, favorites map[string]bool) bool {
	if favoritedOnly && !favorites[item.ID] {
		return false
	}
	for _, tag := range tags {
		if !containsString(item.Tags, tag) {
			return false
		}
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return true
	}
	haystack := strings.ToLower(item.EffectiveTitle()) + "\n" + strings.ToLower(item.EffectiveSummary())
	return strings.Contains(haystack, needle)
}

// sortViews 按**有效标题的字典序**排，标题相同时按标识。
//
// 排序必须确定：没有"按相关度"或"按热度"这类排序——它们要么依赖一套只有搜索引擎
// 才有的相关性模型，要么让列表随使用量变动而变动，而两者都会让"我上次看到的第三
// 条"变成一个无法复现的位置。
func sortViews(views []View) {
	sort.Slice(views, func(i, j int) bool {
		left, right := views[i].EffectiveTitle(), views[j].EffectiveTitle()
		if left != right {
			return left < right
		}
		return views[i].ID < views[j].ID
	})
}

// findFile 在清单里找一条文件。
func findFile(files []File, filePath string) (File, bool) {
	for _, file := range files {
		if file.Path == filePath {
			return file, true
		}
	}
	return File{}, false
}

// containsString 判定一个串是否在集合里。
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// mapObjectError 把对象存储的结论折成领域结论。
//
// **"这个对象不存在"不是"存储不可用"**：前者是一处内容问题（那份字节没有写成），
// 后者是一次故障。两者在用户面前是两句不同的话。
func mapObjectError(err error) error {
	if errors.Is(err, objectstore.ErrObjectNotFound) {
		return fmt.Errorf("%w: 内容对象不存在", ErrPackageInvalid)
	}
	return fmt.Errorf("%w: %v", ErrObjectStoreUnavailable, err)
}
