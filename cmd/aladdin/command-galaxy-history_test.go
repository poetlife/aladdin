package main

import (
	"errors"
	"testing"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
)

// 签名的判据是"路径 + 字节从哪来"，**顺序不参与**：手里那一组是"文件 + 记号登记"
// 两步拼出来的，而服务端存下来的是排好序的清单。两处各排一次就会漂，而漂的表现
// 是"明明没改，却被提示尚未存为版本"。
func TestEntriesSignatureIgnoresOrder(t *testing.T) {
	left := []*galaxyv1.FileEntry{
		{Path: "index.html", Source: &galaxyv1.FileEntry_Digest{Digest: "aa"}},
		{Path: "a.png", Source: &galaxyv1.FileEntry_AssetId{AssetId: "ast_1"}},
	}
	right := []*galaxyv1.FileEntry{
		{Path: "a.png", Source: &galaxyv1.FileEntry_AssetId{AssetId: "ast_1"}},
		{Path: "index.html", Source: &galaxyv1.FileEntry_Digest{Digest: "aa"}},
	}
	if entriesSignature(left) != entriesSignature(right) {
		t.Error("顺序不同不该被当成两份不同的清单")
	}

	// 内容变了就要不同：换个摘要、换个资产、加一条、少一条，四种都算变了。
	cases := map[string][]*galaxyv1.FileEntry{
		"换摘要": {{Path: "index.html", Source: &galaxyv1.FileEntry_Digest{Digest: "bb"}}, right[0]},
		"换资产": {{Path: "index.html", Source: &galaxyv1.FileEntry_Digest{Digest: "aa"}}, {Path: "a.png", Source: &galaxyv1.FileEntry_AssetId{AssetId: "ast_2"}}},
		"少一条": {right[1]},
		"多一条": append(append([]*galaxyv1.FileEntry{}, right...), &galaxyv1.FileEntry{Path: "b.css", Source: &galaxyv1.FileEntry_Digest{Digest: "cc"}}),
	}
	base := entriesSignature(left)
	for name, entries := range cases {
		if entriesSignature(entries) == base {
			t.Errorf("%s：签名应当不同", name)
		}
	}
}

// 空清单的版本与空草稿是同一份：两处都为空时它是"什么都没变"，不该因此提示。
func TestSameAsNewestVersion(t *testing.T) {
	if sameAsNewestVersion(nil, nil) {
		t.Error("一条版本都没有时，不该说'与最新版本一致'")
	}
	versions := []*galaxyv1.Version{
		{Id: "ver_1", Seq: 1},
		{Id: "ver_2", Seq: 2, Entries: []*galaxyv1.FileEntry{
			{Path: "index.html", Source: &galaxyv1.FileEntry_Digest{Digest: "aa"}},
		}},
	}
	entries := []*galaxyv1.FileEntry{
		{Path: "index.html", Source: &galaxyv1.FileEntry_Digest{Digest: "aa"}},
	}
	if !sameAsNewestVersion(versions, entries) {
		t.Error("与**序号最大**的那一版一致时应当判为同一份")
	}
	changed := []*galaxyv1.FileEntry{
		{Path: "index.html", Source: &galaxyv1.FileEntry_Digest{Digest: "bb"}},
	}
	if sameAsNewestVersion(versions, changed) {
		t.Error("内容变了就不该判为同一份")
	}
	if !sameAsNewestVersion([]*galaxyv1.Version{{Id: "ver_1", Seq: 1}}, nil) {
		t.Error("两处都为空时是同一份")
	}
}

// 快照的来源要写成人读的一句话，**空值如实说"未知来源"**，而不是猜一个。
func TestDescribeSnapshotSource(t *testing.T) {
	cases := map[string]string{
		"web": "网页端",
		"cli": "命令行",
		"":    "未知来源",
		"其他端": "未知来源",
	}
	for source, want := range cases {
		if got := describeSnapshotSource(source); got != want {
			t.Errorf("describeSnapshotSource(%q) = %q，期望 %q", source, got, want)
		}
	}
}

// version describe 必须给出 --description：不给标志是一次没有任何改动的调用，
// 在发起请求之前就该被拦下（与 asset update 同一条取向）。
func TestVersionDescribeRequiresDescription(t *testing.T) {
	err := executeRoot(t, "galaxy", "version", "describe", "prj_x", "ver_y")
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("不给 --description 时必须是用法错误，实际：%v", err)
	}
}

// draft restore 与 draft history 的位置参数个数不对时，落到"用法错误"这一个退出码。
func TestDraftHistoryAndRestoreArgCount(t *testing.T) {
	cases := [][]string{
		{"galaxy", "draft", "history"},
		{"galaxy", "draft", "history", "prj_1", "多出来的"},
		{"galaxy", "draft", "restore", "prj_1"},
		{"galaxy", "draft", "restore", "prj_1", "snp_1", "多出来的"},
		{"galaxy", "version", "save", "prj_1", "多出来的"},
	}
	for _, args := range cases {
		err := executeRoot(t, args...)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v：应当是用法错误，实际：%v", args, err)
		}
	}
}

// draft restore **不是**危险操作（恢复前的那一份会留成历史，因此它可回退），
// 而它确实被登记在命令树上、也声明了权限。
func TestDraftSubcommandsAreRegistered(t *testing.T) {
	root := newRootCommand()
	galaxyCmd, _, err := root.Find([]string{"galaxy"})
	if err != nil {
		t.Fatalf("找不到 galaxy 命令：%v", err)
	}
	draftCmd, _, err := galaxyCmd.Find([]string{"draft"})
	if err != nil {
		t.Fatalf("找不到 draft 命令：%v", err)
	}
	got := map[string]bool{}
	for _, child := range draftCmd.Commands() {
		got[child.Name()] = true
	}
	for _, name := range []string{"list", "pull", "push", "history", "restore"} {
		if !got[name] {
			t.Errorf("draft 下缺少子命令 %q", name)
		}
	}
}
