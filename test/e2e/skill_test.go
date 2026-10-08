//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	skillv1 "github.com/poetlife/aladdin/api/gen/aladdin/skill/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/skill/v1/skillv1connect"
	"github.com/poetlife/aladdin/internal/imagetype"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/skill"
)

// 平台技能目录的端到端链路：管理员从远端纳管 → 创作者搜到并取用 → 使用量记上 →
// 同步与回滚对下一次取用立刻生效。
//
// **远端是假的。** 真实实现对真实 GitHub 发请求，那不属于端到端测试的范围（见
// docs/design/skill/onboarding.md 的可验证性表）；这里注入一个可编程的假远端，
// 于是"哪一棵树、哪个提交"由用例说了算。

const (
	skillCommit1 = "1f4a9c2d3e5b6a7089abcdef1234567890abcdef"
	skillCommit2 = "abcdef1234567890abcdef1234567890abcdef12"
	skillOwner   = "yanliudesign"
	skillRepo    = "mono-color-skill"
)

// fakeSkillRemote 是一个可编程的远端：给定提交返回给定的树。
//
// 它带锁：请求跑在服务端的 goroutine 上，而用例会同时改它。
type fakeSkillRemote struct {
	mu     sync.Mutex
	commit string
	trees  map[string]map[string]string
	// blobs 是 FetchTree 走过的对象（假的标识 → 字节），供 FetchBlob 反查。
	blobs map[string][]byte
}

func newFakeSkillRemote() *fakeSkillRemote {
	return &fakeSkillRemote{
		commit: skillCommit1,
		trees:  map[string]map[string]string{},
		blobs:  map[string][]byte{},
	}
}

func (f *fakeSkillRemote) setTree(commit string, files map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commit = commit
	f.trees[commit] = files
}

func (f *fakeSkillRemote) tree(commit string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tree, ok := f.trees[commit]
	if !ok {
		return nil, fmt.Errorf("远端没有这个提交")
	}
	return tree, nil
}

func (f *fakeSkillRemote) ResolveCommit(_ context.Context, repo skill.Repository, _ string) (string, error) {
	if repo.Owner != skillOwner || repo.Name != skillRepo {
		return "", fmt.Errorf("远端没有这个仓库")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commit, nil
}

func (f *fakeSkillRemote) FetchTree(_ context.Context, repo skill.Repository, commit, subPath string) (skill.Tree, error) {
	tree, err := f.tree(commit)
	if err != nil {
		return skill.Tree{}, err
	}
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var out skill.Tree
	for _, path := range paths {
		if subPath != "" && path != subPath && !strings.HasPrefix(path, subPath+"/") {
			continue
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(path, subPath), "/")
		data := []byte(tree[path])
		sha := "sha-" + path
		f.setBlob(sha, data)
		out.Blobs = append(out.Blobs, skill.Blob{Path: relative, SHA: sha, Size: int64(len(data))})
		// 与真实实现同一条规则：不是文本的条目**跳过并点名**，不进包（见
		// docs/design/skill/onboarding.md）。
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			out.Skipped = append(out.Skipped, relative)
			continue
		}
		out.Files = append(out.Files, skill.FetchedFile{Path: relative, Data: data})
	}
	return out, nil
}

func (f *fakeSkillRemote) setBlob(sha string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[sha] = data
}

// FetchBlob 按 FetchTree 给过的标识取字节（展示图的那条路要用）。
func (f *fakeSkillRemote) FetchBlob(_ context.Context, _ skill.Repository, sha string, maxBytes int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.blobs[sha]
	if !ok {
		return nil, fmt.Errorf("%w: 没有这个对象", skill.ErrRepositoryNotFound)
	}
	return data, nil
}

// commandWithHome 构造一条把 HOME 钉在指定目录上的命令。
//
// **它同时清掉 ALADDIN_* 与 XDG_***：目标地址与凭证由参数给出（优先级最高），
// 而环境里残留的取值会让同一条用例在本机与 CI 上跑出两种结果。
func commandWithHome(t *testing.T, home, binary string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(binary, args...)
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "ALADDIN_") ||
			strings.HasPrefix(entry, "HOME=") ||
			strings.HasPrefix(entry, "XDG_") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "HOME="+home)
	return cmd
}

// coverPNG 是一段最小的 PNG：八个字节的签名加一点内容。魔数核对只看文件头。
var coverPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R'}

// skillTree 造一棵合法的技能包。
func skillTree(description string) map[string]string {
	return map[string]string{
		"SKILL.md": fmt.Sprintf(
			"---\nname: mono-color\ndescription: %s\n---\n\n正文。\n", description),
		"palette.md": "# 色板\n",
	}
}

// skillClients 构造走 Connect 的读面与维护面客户端。
func skillClients(t *testing.T, h harness, token, scope string) (skillv1connect.SkillServiceClient, skillv1connect.SkillAdminServiceClient) {
	t.Helper()
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &headerTransport{
			base:  http.DefaultTransport,
			token: token,
			scope: scope,
		},
	}
	base := "http://" + h.address
	return skillv1connect.NewSkillServiceClient(httpClient, base),
		skillv1connect.NewSkillAdminServiceClient(httpClient, base)
}

// importSkill 纳管一个技能，失败即终止用例。
func importSkill(t *testing.T, admin skillv1connect.SkillAdminServiceClient, tags ...string) *skillv1.Skill {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := admin.ImportSkill(ctx, connect.NewRequest(&skillv1.ImportSkillRequest{
		RepositoryUrl: "https://github.com/" + skillOwner + "/" + skillRepo,
		Ref:           "main",
		Tags:          tags,
	}))
	if err != nil {
		t.Fatalf("纳管失败: %v", err)
	}
	return resp.Msg.GetSkill()
}

// 纳管 → 检索 → 取正文 → 使用量，串成一条链。
//
// 它同时钉住三件事：管理员真的能从远端纳管进来；创作者（另一个角色、另一个主体）
// 真的能搜到并取到正文；取用真的被计进了使用量。
func TestSkillCatalogEndToEnd(t *testing.T) {
	h := startServer(t, rbac.RoleSkillCurator, testScope)
	h.skillRemote.setTree(skillCommit1, skillTree("单色印刷风出图。当用户要一张克制的海报时使用。"))

	reader, admin := skillClients(t, h, testToken, testScope)
	item := importSkill(t, admin, "出图", "排版")
	if !strings.HasPrefix(item.GetId(), "skl_") {
		t.Fatalf("纳管回来的标识 = %q", item.GetId())
	}
	if item.GetTitle() != "mono-color" {
		t.Errorf("有效标题 = %q，期望回退到 SKILL.md 的 name", item.GetTitle())
	}
	if item.GetSource().GetRepositoryUrl() != "https://github.com/"+skillOwner+"/"+skillRepo {
		t.Errorf("来源 = %q", item.GetSource().GetRepositoryUrl())
	}
	if item.GetCurrentVersionId() == "" || len(item.GetFiles()) != 2 {
		t.Errorf("详情里的当前版本或文件清单不完整: %+v", item)
	}

	// 第二个主体：能读目录，但不能维护（它拿的是创作者角色）。
	creatorToken, _ := injectSubject(t, h, rbac.RoleGalaxyAuthor, "e2e-skill-reader")
	creatorReader, creatorAdmin := skillClients(t, h, creatorToken, testScope)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// **创作者搜得到**——否则这个目录对他不存在。
	list, err := creatorReader.ListSkills(ctx, connect.NewRequest(&skillv1.ListSkillsRequest{
		Query: "单色",
	}))
	if err != nil {
		t.Fatalf("创作者检索失败: %v", err)
	}
	if len(list.Msg.GetSkills()) != 1 {
		t.Fatalf("创作者搜到 %d 条，期望 1 条", len(list.Msg.GetSkills()))
	}
	if tags := list.Msg.GetSkills()[0].GetTags(); len(tags) != 2 {
		t.Errorf("标签 = %v", tags)
	}
	if len(list.Msg.GetAvailableTags()) != 2 {
		t.Errorf("候选标签 = %v", list.Msg.GetAvailableTags())
	}

	// **创作者不能纳管。**
	if _, err := creatorAdmin.ImportSkill(ctx, connect.NewRequest(&skillv1.ImportSkillRequest{
		RepositoryUrl: "https://github.com/o/r",
	})); err == nil {
		t.Error("没有写权限的主体纳管成功了")
	}

	// 取正文：这是"取用"，也是一次使用。
	file, err := creatorReader.GetSkillFile(ctx, connect.NewRequest(&skillv1.GetSkillFileRequest{
		SkillId: item.GetId(),
		Path:    "palette.md",
	}))
	if err != nil {
		t.Fatalf("取正文失败: %v", err)
	}
	if string(file.Msg.GetContent()) != "# 色板\n" {
		t.Errorf("正文 = %q", file.Msg.GetContent())
	}
	if file.Msg.GetDigest() == "" {
		t.Error("取用没有回显摘要")
	}

	// 取一条不存在的路径：结论与"技能不存在"不同。
	if _, err := creatorReader.GetSkillFile(ctx, connect.NewRequest(&skillv1.GetSkillFileRequest{
		SkillId: item.GetId(), Path: "nope.md",
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("不存在的路径 err = %v", err)
	} else if strings.Contains(err.Error(), "技能不存在") {
		t.Errorf("不存在的路径被报成了技能不存在: %v", err)
	}

	// 使用量：创作者取过一次，管理员没有。
	view, err := creatorReader.GetSkill(ctx, connect.NewRequest(&skillv1.GetSkillRequest{
		SkillId: item.GetId(),
	}))
	if err != nil {
		t.Fatalf("读取详情失败: %v", err)
	}
	if got := view.Msg.GetSkill().GetUsage().GetUseDays(); got != 1 {
		t.Errorf("创作者看到的使用日次 = %d，期望 1", got)
	}
	if got := view.Msg.GetSkill().GetUsage().GetUserCount(); got != 1 {
		t.Errorf("使用人数 = %d，期望 1", got)
	}
	adminView, err := reader.GetSkill(ctx, connect.NewRequest(&skillv1.GetSkillRequest{
		SkillId: item.GetId(),
	}))
	if err != nil {
		t.Fatalf("读取详情失败: %v", err)
	}
	if adminView.Msg.GetSkill().GetFavorited() {
		t.Error("别人取用过之后，我的收藏状态变了")
	}

	// 收藏是主体级的。
	if _, err := creatorReader.SetSkillFavorite(ctx, connect.NewRequest(&skillv1.SetSkillFavoriteRequest{
		SkillId: item.GetId(), Favorited: true,
	})); err != nil {
		t.Fatalf("收藏失败: %v", err)
	}
	mine, err := creatorReader.ListSkills(ctx, connect.NewRequest(&skillv1.ListSkillsRequest{
		FavoritedOnly: true,
	}))
	if err != nil {
		t.Fatalf("列出收藏失败: %v", err)
	}
	if len(mine.Msg.GetSkills()) != 1 {
		t.Errorf("我看自己的收藏得到 %d 条", len(mine.Msg.GetSkills()))
	}
	theirs, err := reader.ListSkills(ctx, connect.NewRequest(&skillv1.ListSkillsRequest{
		FavoritedOnly: true,
	}))
	if err != nil {
		t.Fatalf("列出收藏失败: %v", err)
	}
	if len(theirs.Msg.GetSkills()) != 0 {
		t.Errorf("别人的收藏影响到了我: %d 条", len(theirs.Msg.GetSkills()))
	}
}

// 同步产生新版本并生效，回滚把它切回去——**对下一次取用立刻生效**。
func TestSkillSyncAndRollbackTakeEffectImmediately(t *testing.T) {
	h := startServer(t, rbac.RoleSkillCurator, testScope)
	h.skillRemote.setTree(skillCommit1, skillTree("第一版的触发说明。"))

	reader, admin := skillClients(t, h, testToken, testScope)
	item := importSkill(t, admin)
	firstVersion := item.GetCurrentVersionId()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 没有变化时同步是空操作。
	resync, err := admin.ResyncSkill(ctx, connect.NewRequest(&skillv1.ResyncSkillRequest{
		SkillId: item.GetId(),
	}))
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if resync.Msg.GetChanged() {
		t.Error("远端没有变化却报告有变化")
	}
	if resync.Msg.GetSkill().GetCurrentVersionId() != firstVersion {
		t.Error("没有变化时换了版本")
	}

	// 远端有了新提交。
	h.skillRemote.setTree(skillCommit2, map[string]string{
		"SKILL.md":   "---\nname: mono-color\ndescription: 第二版的触发说明。\n---\n\n正文。\n",
		"palette.md": "# 第二版色板\n",
	})
	resync, err = admin.ResyncSkill(ctx, connect.NewRequest(&skillv1.ResyncSkillRequest{
		SkillId: item.GetId(),
	}))
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !resync.Msg.GetChanged() {
		t.Fatal("远端有新提交却报告没有变化")
	}
	secondVersion := resync.Msg.GetSkill().GetCurrentVersionId()
	if secondVersion == firstVersion {
		t.Fatal("同步没有换版本")
	}
	if got := resync.Msg.GetSkill().GetSource().GetCommit(); got != skillCommit2 {
		t.Errorf("记录的提交 = %q", got)
	}
	assertSkillFile(t, reader, item.GetId(), "palette.md", "# 第二版色板\n")

	versions, err := reader.ListSkillVersions(ctx, connect.NewRequest(&skillv1.ListSkillVersionsRequest{
		SkillId: item.GetId(),
	}))
	if err != nil {
		t.Fatalf("列出版本失败: %v", err)
	}
	if len(versions.Msg.GetVersions()) != 2 {
		t.Fatalf("版本数 = %d，期望 2", len(versions.Msg.GetVersions()))
	}
	if !versions.Msg.GetVersions()[0].GetCurrent() {
		t.Error("最新的那一版没有被标成当前")
	}

	// 回滚：切指针，**取到的正文立刻变回第一版**。
	if _, err := admin.SetCurrentSkillVersion(ctx, connect.NewRequest(&skillv1.SetCurrentSkillVersionRequest{
		SkillId: item.GetId(), VersionId: firstVersion,
	})); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	assertSkillFile(t, reader, item.GetId(), "palette.md", "# 色板\n")

	// **回滚不被同步撤销**：远端提交没变，同步什么都不做，指针留在回滚后的位置。
	// 一次习惯性的同步能悄悄取消回滚，是这条规则要挡住的东西。
	resync, err = admin.ResyncSkill(ctx, connect.NewRequest(&skillv1.ResyncSkillRequest{
		SkillId: item.GetId(),
	}))
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if resync.Msg.GetChanged() {
		t.Error("远端提交没变，同步不该报告有变化")
	}
	if got := resync.Msg.GetSkill().GetCurrentVersionId(); got != firstVersion {
		t.Errorf("回滚之后同步把指针挪走了：%q", got)
	}
	assertSkillFile(t, reader, item.GetId(), "palette.md", "# 色板\n")

	// 要回到最新就再切一次指针——版本列表里它排在最前面。
	if _, err := admin.SetCurrentSkillVersion(ctx, connect.NewRequest(&skillv1.SetCurrentSkillVersionRequest{
		SkillId: item.GetId(), VersionId: secondVersion,
	})); err != nil {
		t.Fatalf("切回最新失败: %v", err)
	}
	assertSkillFile(t, reader, item.GetId(), "palette.md", "# 第二版色板\n")
}

// 校验失败**不留痕迹**：被拒的纳管之后目录里什么都查不到。
func TestSkillImportRejectionLeavesNoTrace(t *testing.T) {
	h := startServer(t, rbac.RoleSkillCurator, testScope)
	h.skillRemote.setTree(skillCommit1, map[string]string{"readme.md": "# 没有清单\n"})

	reader, admin := skillClients(t, h, testToken, testScope)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := admin.ImportSkill(ctx, connect.NewRequest(&skillv1.ImportSkillRequest{
		RepositoryUrl: "https://github.com/" + skillOwner + "/" + skillRepo,
	})); err == nil {
		t.Fatal("缺 SKILL.md 的包被接受了")
	}

	list, err := reader.ListSkills(ctx, connect.NewRequest(&skillv1.ListSkillsRequest{}))
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if len(list.Msg.GetSkills()) != 0 {
		t.Errorf("被拒的纳管留下了 %d 条技能", len(list.Msg.GetSkills()))
	}
}

// 地址形状不合法在**解析阶段**就被拒，不经过任何远端。
func TestSkillImportRejectsNonGithubAddress(t *testing.T) {
	h := startServer(t, rbac.RoleSkillCurator, testScope)
	_, admin := skillClients(t, h, testToken, testScope)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, address := range []string{
		"https://gitlab.com/owner/repo",
		"https://github.com.evil.example/owner/repo",
		"http://github.com/owner/repo",
		"https://github.com/owner/repo/tree/main/skills/x",
	} {
		if _, err := admin.ImportSkill(ctx, connect.NewRequest(&skillv1.ImportSkillRequest{
			RepositoryUrl: address,
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("地址 %q 的结论 = %v", address, err)
		}
	}
}

// 展示图集：纳管时从仓库取两张 → 卡片拿首图 → 详情给整组 → 直传加一张 → 换一张 →
// 设为首图 → 删一张。
//
// 它是**说明层的一项**，因此这一条同时要确认：图进来了，而文件清单里没有它们。
func TestSkillImagesEndToEnd(t *testing.T) {
	h := startServer(t, rbac.RoleSkillCurator, testScope)
	h.skillRemote.setTree(skillCommit1, map[string]string{
		"SKILL.md":      "---\nname: mono-color\ndescription: 出图。\n---\n\n正文。\n",
		"gallery/a.png": string(coverPNG) + "a",
		"gallery/b.png": string(coverPNG) + "b",
	})

	reader, admin := skillClients(t, h, testToken, testScope)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := admin.ImportSkill(ctx, connect.NewRequest(&skillv1.ImportSkillRequest{
		RepositoryUrl: "https://github.com/" + skillOwner + "/" + skillRepo,
		ImagePaths:    []string{"gallery/a.png", "gallery/b.png"},
		Tags:          []string{"出图"},
	}))
	if err != nil {
		t.Fatalf("带展示图纳管失败: %v", err)
	}
	item := resp.Msg.GetSkill()
	if len(item.GetImages()) != 2 {
		t.Fatalf("详情里的图集 = %d 张，期望 2 张", len(item.GetImages()))
	}
	// **首图即封面**：cover_url 就是第一张的地址。
	if item.GetCoverUrl() == "" || item.GetCoverUrl() != item.GetImages()[0].GetUrl() {
		t.Errorf("首图地址 = %q，第一张的地址 = %q", item.GetCoverUrl(), item.GetImages()[0].GetUrl())
	}
	if item.GetImages()[0].GetId() == item.GetImages()[1].GetId() {
		t.Error("两张图的标识相同")
	}
	// **展示图不是包的内容**：文件清单里只有那份文本。
	if len(item.GetFiles()) != 1 {
		t.Errorf("文件清单 = %d 条，期望 1 条", len(item.GetFiles()))
	}
	for _, file := range item.GetFiles() {
		if strings.HasPrefix(file.GetPath(), "gallery/") {
			t.Errorf("展示图 %q 进了文件清单", file.GetPath())
		}
	}

	// **列表只给首图**：卡片要用的那一个地址给，整组不给。
	list, err := reader.ListSkills(ctx, connect.NewRequest(&skillv1.ListSkillsRequest{}))
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	listed := list.Msg.GetSkills()[0]
	if listed.GetCoverUrl() != item.GetCoverUrl() {
		t.Errorf("列表里的首图地址 = %q，期望 %q", listed.GetCoverUrl(), item.GetCoverUrl())
	}
	if len(listed.GetImages()) != 0 {
		t.Errorf("列表里带了 %d 张图，期望一张都不带", len(listed.GetImages()))
	}

	// 直传加一张：签发 → 直传 → 提交，落在**末尾**。
	added := string(coverPNG) + "d"
	begin, err := admin.BeginSkillImageUpload(ctx, connect.NewRequest(&skillv1.BeginSkillImageUploadRequest{
		SkillId: item.GetId(), ContentType: "image/png", SizeBytes: uint64(len(added)),
	}))
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	credential := begin.Msg.GetUpload()
	if credential == nil || credential.GetKey() == "" || begin.Msg.GetImageId() == "" {
		t.Fatal("没有返回图标识或直传凭证")
	}
	// 直传那一步在真实链路里由浏览器把字节写进桶；装配里没有真桶，因此这里直接
	// 写进假存储（与资产那些用例同一条）。
	h.objects.SimulateUpload(credential.GetKey(), []byte(added))
	committed, err := admin.CommitSkillImageUpload(ctx, connect.NewRequest(&skillv1.CommitSkillImageUploadRequest{
		SkillId: item.GetId(), ImageId: begin.Msg.GetImageId(),
	}))
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if got := committed.Msg.GetSkill().GetImages(); len(got) != 3 || got[2].GetId() != begin.Msg.GetImageId() {
		t.Fatalf("加完之后图集 = %+v，期望新的一张排在末尾", got)
	}
	firstKey := objectKeyOf(t, committed.Msg.GetSkill().GetImages()[0].GetUrl())

	// 换第一张（同一标识、同一键、同一位置）。
	replaced := string(coverPNG) + "replaced"
	replace, err := admin.BeginSkillImageUpload(ctx, connect.NewRequest(&skillv1.BeginSkillImageUploadRequest{
		SkillId:     item.GetId(),
		ImageId:     item.GetImages()[0].GetId(),
		ContentType: "image/png",
		SizeBytes:   uint64(len(replaced)),
	}))
	if err != nil {
		t.Fatalf("换图签发失败: %v", err)
	}
	if replace.Msg.GetUpload().GetKey() != firstKey {
		t.Errorf("换图换了一个键: %q → %q", firstKey, replace.Msg.GetUpload().GetKey())
	}
	h.objects.SimulateUpload(replace.Msg.GetUpload().GetKey(), []byte(replaced))
	swapped, err := admin.CommitSkillImageUpload(ctx, connect.NewRequest(&skillv1.CommitSkillImageUploadRequest{
		SkillId: item.GetId(), ImageId: item.GetImages()[0].GetId(),
	}))
	if err != nil {
		t.Fatalf("换图提交失败: %v", err)
	}
	if got := swapped.Msg.GetSkill().GetImages(); len(got) != 3 ||
		got[0].GetId() != item.GetImages()[0].GetId() || got[1].GetId() != item.GetImages()[1].GetId() {
		t.Fatalf("换图之后图集 = %+v，期望位置与标识都不变", got)
	}

	// 设为首图：把最后一张排到第一位，**其余每一张的地址都不变**。
	images := swapped.Msg.GetSkill().GetImages()
	order := []string{images[2].GetId(), images[0].GetId(), images[1].GetId()}
	reordered, err := admin.ReorderSkillImages(ctx, connect.NewRequest(&skillv1.ReorderSkillImagesRequest{
		SkillId: item.GetId(), ImageIds: order,
	}))
	if err != nil {
		t.Fatalf("重排失败: %v", err)
	}
	after := reordered.Msg.GetSkill()
	if after.GetCoverUrl() != images[2].GetUrl() {
		t.Errorf("设为首图之后的首图地址 = %q，期望 %q", after.GetCoverUrl(), images[2].GetUrl())
	}
	for i, wantID := range order {
		if after.GetImages()[i].GetId() != wantID {
			t.Errorf("重排之后第 %d 张 = %q，期望 %q", i, after.GetImages()[i].GetId(), wantID)
		}
	}
	// 被拒的重排：少一张。
	if _, err := admin.ReorderSkillImages(ctx, connect.NewRequest(&skillv1.ReorderSkillImagesRequest{
		SkillId: item.GetId(), ImageIds: order[:2],
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("少一张的重排 = %v，期望 InvalidArgument", err)
	}

	// 删中间一张：剩下的顺序与地址都不变。
	deleted, err := admin.DeleteSkillImage(ctx, connect.NewRequest(&skillv1.DeleteSkillImageRequest{
		SkillId: item.GetId(), ImageId: order[1],
	}))
	if err != nil {
		t.Fatalf("删图失败: %v", err)
	}
	remaining := deleted.Msg.GetSkill().GetImages()
	if len(remaining) != 2 || remaining[0].GetId() != order[0] || remaining[1].GetId() != order[2] {
		t.Fatalf("删完之后图集 = %+v", remaining)
	}
	if remaining[0].GetUrl() != after.GetImages()[0].GetUrl() || remaining[1].GetUrl() != after.GetImages()[2].GetUrl() {
		t.Error("删中间一张改了其余图的地址")
	}
	// 删一张已经不在的：技能在，是那一张不在。
	if _, err := admin.DeleteSkillImage(ctx, connect.NewRequest(&skillv1.DeleteSkillImageRequest{
		SkillId: item.GetId(), ImageId: order[1],
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("重复删 = %v，期望 NotFound", err)
	}

	// **上限由能力下发点给出**，客户端不猜：界面上那句"最多几张"来自这里
	// （见 skill.proto 的 GetCapabilities）。
	caps, err := reader.GetCapabilities(ctx, connect.NewRequest(&skillv1.GetCapabilitiesRequest{}))
	if err != nil {
		t.Fatalf("读取能力失败: %v", err)
	}
	if got := caps.Msg.GetCapabilities(); got.GetMaxImages() != skill.MaxImages ||
		got.GetMaxImageBytes() != imagetype.MaxBytes ||
		got.GetMaxImageTotalBytes() != skill.MaxImageTotalBytes {
		t.Errorf("能力里的图集上限 = %+v，期望 %d / %d / %d",
			got, skill.MaxImages, imagetype.MaxBytes, skill.MaxImageTotalBytes)
	}

	// **写权限是图集那道门。** 创作者能读目录，但图集的四个方法一律被拒——它们改的
	// 是目录内容，与"读"不是一回事。
	creatorToken, _ := injectSubject(t, h, rbac.RoleGalaxyAuthor, "e2e-skill-image-reader")
	_, creatorAdmin := skillClients(t, h, creatorToken, testScope)
	if _, err := creatorAdmin.BeginSkillImageUpload(ctx, connect.NewRequest(&skillv1.BeginSkillImageUploadRequest{
		SkillId: item.GetId(), ContentType: "image/png", SizeBytes: 8,
	})); err == nil {
		t.Error("没有写权限的主体签发了展示图上传播凭证")
	}
	if _, err := creatorAdmin.CommitSkillImageUpload(ctx, connect.NewRequest(&skillv1.CommitSkillImageUploadRequest{
		SkillId: item.GetId(), ImageId: order[0],
	})); err == nil {
		t.Error("没有写权限的主体提交了展示图")
	}
	if _, err := creatorAdmin.DeleteSkillImage(ctx, connect.NewRequest(&skillv1.DeleteSkillImageRequest{
		SkillId: item.GetId(), ImageId: order[0],
	})); err == nil {
		t.Error("没有写权限的主体删除了展示图")
	}
	if _, err := creatorAdmin.ReorderSkillImages(ctx, connect.NewRequest(&skillv1.ReorderSkillImagesRequest{
		SkillId: item.GetId(), ImageIds: order,
	})); err == nil {
		t.Error("没有写权限的主体重排了展示图")
	}

	// **同一套注解在另一条协议上结论相同。** 上面几颗都是 Connect；这里用 grpc-go
	// 客户端再打同一个方法——判定只有一处实现，但那处实现要能在三条协议上都被走到
	// （见 AGENTS.md 的"改判定必须三种协议都对"）。
	grpcCreator := h.dial(t, creatorToken, testScope)
	grpcCtx, grpcCancel := grpcCreator.Context()
	defer grpcCancel()
	if _, err := skillv1.NewSkillAdminServiceClient(grpcCreator.Conn()).
		BeginSkillImageUpload(grpcCtx, &skillv1.BeginSkillImageUploadRequest{
			SkillId: item.GetId(), ContentType: "image/png", SizeBytes: 8,
		}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("gRPC 上无写权限的签发 = %v，期望 PermissionDenied", err)
	}
	grpcAdmin := h.dial(t, testToken, testScope)
	grpcAdminCtx, grpcAdminCancel := grpcAdmin.Context()
	defer grpcAdminCancel()
	if _, err := skillv1.NewSkillAdminServiceClient(grpcAdmin.Conn()).
		BeginSkillImageUpload(grpcAdminCtx, &skillv1.BeginSkillImageUploadRequest{
			SkillId: item.GetId(), ContentType: "image/png", SizeBytes: 8,
		}); err != nil {
		t.Errorf("gRPC 上有写权限的签发失败: %v", err)
	}
}

// objectKeyOf 从假对象存储给出的地址里取回对象键。
//
// 这里能从地址反推键，是因为装配里的对象存储是内存实现；生产实现给的是一个真正的
// 预签名地址，反推不出来——而这条用例要断言的正是"换图没换键、删中间一张没动其余"。
func objectKeyOf(t *testing.T, url string) string {
	t.Helper()
	const prefix = "memory://"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("地址 %q 不是内存实现的形状", url)
	}
	key := strings.TrimPrefix(url, prefix)
	if i := strings.Index(key, "?ttl="); i >= 0 {
		key = key[:i]
	}
	return key
}

// **平台从不写调用方的文件系统**：跑完取用之后，调用方的 HOME 里不出现任何技能
// 目录或文件。
func TestSkillCommandsDoNotWriteToDisk(t *testing.T) {
	h := startServer(t, rbac.RoleSkillCurator, testScope)
	h.skillRemote.setTree(skillCommit1, skillTree("单色印刷风出图。"))
	binary := buildCLI(t)

	_, admin := skillClients(t, h, testToken, testScope)
	item := importSkill(t, admin)

	home := t.TempDir()
	before := listTree(t, home)
	if len(before) != 0 {
		t.Fatalf("临时 HOME 不是空的: %v", before)
	}

	for _, args := range [][]string{
		{"skill", "list"},
		{"skill", "get", item.GetId()},
		{"skill", "cat", item.GetId(), "palette.md"},
		{"skill", "versions", item.GetId()},
	} {
		runCLIWithHome(t, binary, h, home, args...)
	}

	after := listTree(t, home)
	if len(after) != 0 {
		t.Errorf("跑完之后 HOME 里出现了东西: %v", after)
	}
	// 几条最典型的"装进本机"的落点，一个都不该存在。
	for _, forbidden := range []string{".claude", ".cursor", ".codex", ".aladdin/skills"} {
		if _, err := os.Stat(filepath.Join(home, forbidden)); err == nil {
			t.Errorf("HOME 下出现了 %s", forbidden)
		}
	}
}

// runCLIWithHome 在指定的 HOME 下执行一次命令行。
//
// `runCLI` 不带 HOME，而这条用例要断言的正是"它没往那里写东西"，因此必须把它
// 钉在一个空目录上。参数优先级最高，凭证与目标地址照旧由参数给出。
func runCLIWithHome(t *testing.T, binary string, h harness, home string, args ...string) string {
	t.Helper()
	full := append([]string{
		"--address", h.address,
		"--token", testToken,
		"--output", "json",
		"--yes",
	}, args...)
	cmd := commandWithHome(t, home, binary, full...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("命令行 %v 失败: %v\n%s", args, err, stderr.String())
	}
	return stdout.String()
}

// listTree 返回一棵目录树里全部相对路径（升序）。目录本身也算一条：一个空目录的
// 出现同样是"平台往这里写了东西"。
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	sort.Strings(paths)
	return paths
}

// injectSubject 注入第二个主体并绑定一个角色，返回它的凭证。
//
// 与生产完全相同的入口：凭证表与主体存储。**不直接写绑定之外的东西**，因此"它能
// 做什么"就是 RBAC 判出来的结论。
func injectSubject(t *testing.T, h harness, roleID string, subjectID string) (string, rbac.Subject) {
	t.Helper()
	token := subjectID + "-token"
	subject := rbac.Subject{ID: subjectID, Type: rbac.SubjectTypeUser, DefaultScope: testScope}
	h.srv.Authenticator().Add(token, subject)
	ctx := context.Background()
	if err := h.srv.Store().PutSubject(ctx, subject); err != nil {
		t.Fatalf("注入主体失败: %v", err)
	}
	if err := h.srv.Store().Bind(ctx, rbac.RoleBinding{
		SubjectID: subjectID, RoleID: roleID, Scope: testScope,
	}); err != nil {
		t.Fatalf("绑定角色失败: %v", err)
	}
	return token, subject
}

// assertSkillFile 取一条文件的字节并断言内容。
func assertSkillFile(t *testing.T, reader skillv1connect.SkillServiceClient, skillID, path, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := reader.GetSkillFile(ctx, connect.NewRequest(&skillv1.GetSkillFileRequest{
		SkillId: skillID, Path: path,
	}))
	if err != nil {
		t.Fatalf("取 %s 失败: %v", path, err)
	}
	if string(resp.Msg.GetContent()) != want {
		t.Errorf("%s 的正文 = %q，期望 %q", path, resp.Msg.GetContent(), want)
	}
}
