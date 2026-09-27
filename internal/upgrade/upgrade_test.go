package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ------------------------------------------------------------ 版本号

func TestParseVersionAcceptsOnlyStrictReleaseForm(t *testing.T) {
	valid := map[string]Version{
		"v0.4.3":  {Major: 0, Minor: 4, Patch: 3},
		"v1.2.3":  {Major: 1, Minor: 2, Patch: 3},
		"v10.0.0": {Major: 10},
		// 构建期注入的取值可能带空白，去掉它不影响严格性。
		" v1.0.0\n": {Major: 1},
	}
	for input, want := range valid {
		got, ok := ParseVersion(input)
		if !ok {
			t.Errorf("%q 应当解析成功", input)
			continue
		}
		if got != want {
			t.Errorf("%q 解析为 %+v，期望 %+v", input, got, want)
		}
	}

	invalid := []string{
		"", "dev", "1.2.3", "v1.2", "v1.2.3.4",
		"v01.2.3",       // 前导零：同一个版本会有两种写法
		"v1.2.3-rc.1",   // 预发布后缀
		"v1.2.3+build",  // 构建元数据
		"v0.4.3-5-gabc", // git describe 的形态
		"v1.2.3-dirty",  // 本机构建
		"vx.y.z",
		"v-1.2.3",
	}
	for _, input := range invalid {
		if _, ok := ParseVersion(input); ok {
			t.Errorf("%q 不该被当成发布版本", input)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.1.0", "v1.0.9", 1},
		{"v2.0.0", "v1.99.99", 1},
	}
	for _, tc := range cases {
		a, _ := ParseVersion(tc.a)
		b, _ := ParseVersion(tc.b)
		got := a.Compare(b)
		if (got < 0) != (tc.want < 0) || (got > 0) != (tc.want > 0) {
			t.Errorf("%s 与 %s 比较得到 %d，期望符号与 %d 一致", tc.a, tc.b, got, tc.want)
		}
	}
}

// failTransport 在任何一次出站请求上让用例失败。
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Error("这条路径不该发出网络请求")
	return nil, errors.New("不应到达这里")
}

// 本机构建被拒绝，且**拒绝发生在任何网络请求之前**。
func TestNewRejectsNonReleaseVersionsWithoutNetwork(t *testing.T) {
	for _, version := range []string{"dev", "v0.4.3-5-gabc1234", "v1.0.0-dirty", ""} {
		_, err := New(Options{
			Current:    version,
			HTTPClient: &http.Client{Transport: failTransport{t: t}},
		})
		if !errors.Is(err, ErrNotReleased) {
			t.Errorf("版本 %q 应被拒绝，实际 %v", version, err)
		}
	}
}

// ------------------------------------------------------------ 假发布源

// fakeRelease 是一个本机的假发布源。
type fakeRelease struct {
	server *httptest.Server
	tag    string
	assets map[string][]byte
}

func newFakeRelease(t *testing.T, tag string, assets map[string][]byte) *fakeRelease {
	t.Helper()

	f := &fakeRelease{tag: tag, assets: assets}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Owner+"/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("出站请求没有带 User-Agent：GitHub 会直接拒绝")
		}
		names := make([]string, 0, len(f.assets))
		for name := range f.assets {
			names = append(names, name)
		}
		sort.Strings(names)
		list := make([]map[string]string, 0, len(names))
		for _, name := range names {
			list = append(list, map[string]string{
				"name":                 name,
				"browser_download_url": f.server.URL + "/download/" + name,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "assets": list})
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		content, ok := f.assets[strings.TrimPrefix(r.URL.Path, "/download/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// tarGz 造一个与 release-build 同形的产物包：根下一个普通文件。
func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o755,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("写 tar 头失败: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("写 tar 内容失败: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
	return buf.Bytes()
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// cliAssets 造一份"某版本某个平台"的完整发布：产物包 + 匹配的校验和清单。
func cliAssets(t *testing.T, tag, goos, goarch string, binary []byte) map[string][]byte {
	t.Helper()

	name := assetName(tag, goos, goarch)
	archive := tarGz(t, "aladdin", binary)
	return map[string][]byte{
		name:                      archive,
		checksumsAsset:            []byte(sha256Hex(archive) + "  " + name + "\n"),
		"aladdin-server_x.tar.gz": []byte("与本平台无关的产物"),
	}
}

const (
	testGOOS   = "darwin"
	testGOARCH = "arm64"
)

func testUpdater(t *testing.T, current, exePath string, release *fakeRelease) Updater {
	t.Helper()

	updater, err := New(Options{
		Current:        current,
		GOOS:           testGOOS,
		GOARCH:         testGOARCH,
		ExecutablePath: exePath,
		APIBaseURL:     release.server.URL,
	})
	if err != nil {
		t.Fatalf("构造自更新入口失败: %v", err)
	}
	return updater
}

// ------------------------------------------------------------ 决定要不要升级

func TestResolveReportsUpToDate(t *testing.T) {
	release := newFakeRelease(t, "v0.4.3", cliAssets(t, "v0.4.3", testGOOS, testGOARCH, []byte("新")))
	updater := testUpdater(t, "v0.4.3", "", release)

	decision, err := updater.Resolve(context.Background())
	if err != nil {
		t.Fatalf("比较失败: %v", err)
	}
	if decision.Status != StatusUpToDate {
		t.Fatalf("应为已是最新，实际 %v", decision.Status)
	}
}

// 本机更高时如实说明，且**不降级**。
func TestResolveDoesNotDowngrade(t *testing.T) {
	release := newFakeRelease(t, "v0.4.3", cliAssets(t, "v0.4.3", testGOOS, testGOARCH, []byte("旧")))
	updater := testUpdater(t, "v0.5.0", "", release)

	decision, err := updater.Resolve(context.Background())
	if err != nil {
		t.Fatalf("比较失败: %v", err)
	}
	if decision.Status != StatusAhead {
		t.Fatalf("本机更高时应为 StatusAhead，实际 %v", decision.Status)
	}

	_, err = updater.Upgrade(context.Background(), decision)
	if err == nil {
		t.Fatal("本机更高时不该发生升级")
	}
}

func TestResolvePicksPlatformAsset(t *testing.T) {
	release := newFakeRelease(t, "v0.6.0", cliAssets(t, "v0.6.0", testGOOS, testGOARCH, []byte("新")))
	updater := testUpdater(t, "v0.4.3", "", release)

	decision, err := updater.Resolve(context.Background())
	if err != nil {
		t.Fatalf("比较失败: %v", err)
	}
	if decision.Status != StatusOutdated {
		t.Fatalf("应为有新版可用，实际 %v", decision.Status)
	}
	if want := assetName("v0.6.0", testGOOS, testGOARCH); decision.Asset != want {
		t.Fatalf("挑中的产物是 %q，期望 %q", decision.Asset, want)
	}
}

// 本平台没有产物时，错误信息要列出支持的平台。
func TestResolveWithoutPlatformAssetListsSupported(t *testing.T) {
	release := newFakeRelease(t, "v0.6.0", cliAssets(t, "v0.6.0", "linux", "amd64", []byte("新")))
	updater := testUpdater(t, "v0.4.3", "", release)

	_, err := updater.Resolve(context.Background())
	if err == nil {
		t.Fatal("本平台没有产物时应当失败")
	}
	for _, platform := range SupportedPlatforms {
		if !strings.Contains(err.Error(), platform) {
			t.Errorf("错误信息里没有列出 %s：%v", platform, err)
		}
	}
}

func TestResolveWithoutChecksumsFails(t *testing.T) {
	name := assetName("v0.6.0", testGOOS, testGOARCH)
	release := newFakeRelease(t, "v0.6.0", map[string][]byte{
		name: tarGz(t, "aladdin", []byte("新")),
	})
	updater := testUpdater(t, "v0.4.3", "", release)

	if _, err := updater.Resolve(context.Background()); err == nil {
		t.Fatal("没有校验和清单时应当失败")
	}
}

func TestResolveRejectsRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	updater, err := New(Options{Current: "v0.4.3", GOOS: testGOOS, GOARCH: testGOARCH, APIBaseURL: server.URL})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	_, err = updater.Resolve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "频率") {
		t.Fatalf("频率限制应给出可操作的提示，实际 %v", err)
	}
}

// ------------------------------------------------------------ 升级

// 造一个装了"旧版本"的临时目录，返回可执行文件的路径。
func installedBinary(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "aladdin")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("写二进制失败: %v", err)
	}
	return path
}

func TestUpgradeReplacesBinary(t *testing.T) {
	binary := []byte("我是新的二进制")
	release := newFakeRelease(t, "v0.6.0", cliAssets(t, "v0.6.0", testGOOS, testGOARCH, binary))
	path := installedBinary(t, "我是旧的二进制")
	updater := testUpdater(t, "v0.4.3", path, release)

	decision, err := updater.Resolve(context.Background())
	if err != nil {
		t.Fatalf("比较失败: %v", err)
	}
	result, err := updater.Upgrade(context.Background(), decision)
	if err != nil {
		t.Fatalf("升级失败: %v", err)
	}
	if result.From.String() != "v0.4.3" || result.To.String() != "v0.6.0" {
		t.Fatalf("升级结果记的是 %s → %s", result.From, result.To)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回二进制失败: %v", err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("二进制没有被替换：%q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("取文件信息失败: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("替换后的文件不可执行：%v", info.Mode())
	}

	// 可执行目录里不该留下任何临时文件。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("读目录失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("目录里残留了别的文件：%v", entries)
	}
}

// 校验和不符时替换不发生，本机二进制一个字节都不变。
func TestUpgradeFailsClosedOnChecksumMismatch(t *testing.T) {
	binary := []byte("我是新的二进制")
	name := assetName("v0.6.0", testGOOS, testGOARCH)
	release := newFakeRelease(t, "v0.6.0", map[string][]byte{
		name:           tarGz(t, "aladdin", binary),
		checksumsAsset: []byte(sha256Hex([]byte("别的字节")) + "  " + name + "\n"),
	})
	path := installedBinary(t, "我是旧的二进制")
	updater := testUpdater(t, "v0.4.3", path, release)

	decision, err := updater.Resolve(context.Background())
	if err != nil {
		t.Fatalf("比较失败: %v", err)
	}
	if _, err := updater.Upgrade(context.Background(), decision); err == nil {
		t.Fatal("校验和不符时应当失败")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回二进制失败: %v", err)
	}
	if string(got) != "我是旧的二进制" {
		t.Fatalf("校验失败却改了二进制：%q", got)
	}
}

// 清单里没有本产物时失败，**不退化成跳过校验**。
func TestUpgradeFailsWhenChecksumEntryMissing(t *testing.T) {
	name := assetName("v0.6.0", testGOOS, testGOARCH)
	release := newFakeRelease(t, "v0.6.0", map[string][]byte{
		name:           tarGz(t, "aladdin", []byte("新")),
		checksumsAsset: []byte(sha256Hex([]byte("别人")) + "  aladdin-server_v0.6.0_x.tar.gz\n"),
	})
	path := installedBinary(t, "旧")
	updater := testUpdater(t, "v0.4.3", path, release)

	decision, err := updater.Resolve(context.Background())
	if err != nil {
		t.Fatalf("比较失败: %v", err)
	}
	if _, err := updater.Upgrade(context.Background(), decision); err == nil {
		t.Fatal("清单里没有本产物时应当失败，而不是跳过校验")
	}
	if got, _ := os.ReadFile(path); string(got) != "旧" {
		t.Fatalf("失败了却改了二进制：%q", got)
	}
}

// 以符号链接启动时，换的是链接指向的那个文件。
func TestUpgradeFollowsSymlink(t *testing.T) {
	binary := []byte("我是新的二进制")
	release := newFakeRelease(t, "v0.6.0", cliAssets(t, "v0.6.0", testGOOS, testGOARCH, binary))

	dir := t.TempDir()
	real := filepath.Join(dir, "aladdin-real")
	if err := os.WriteFile(real, []byte("旧"), 0o755); err != nil {
		t.Fatalf("写二进制失败: %v", err)
	}
	link := filepath.Join(dir, "aladdin")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("建符号链接失败: %v", err)
	}

	updater := testUpdater(t, "v0.4.3", link, release)
	decision, err := updater.Resolve(context.Background())
	if err != nil {
		t.Fatalf("比较失败: %v", err)
	}
	if _, err := updater.Upgrade(context.Background(), decision); err != nil {
		t.Fatalf("升级失败: %v", err)
	}

	// 链接还是链接，指向的文件已经是新版本。
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("取链接信息失败: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("符号链接被换成了普通文件：使用者的目录布局被改掉了")
	}
	if got, err := os.ReadFile(real); err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("链接指向的文件没有被替换：%q（%v）", got, err)
	}
}

// 目录不可写时给出可操作的错误，且不改变现有文件。
func TestReplaceOnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时目录权限不起作用")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "aladdin")
	if err := os.WriteFile(path, []byte("旧"), 0o755); err != nil {
		t.Fatalf("写二进制失败: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("改目录权限失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := Replace(path, []byte("新"))
	if err == nil {
		t.Fatal("目录不可写时应当失败")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("错误信息里应指出是哪个目录：%v", err)
	}
}

// ------------------------------------------------------------ 解包

func TestExtractBinary(t *testing.T) {
	binary := []byte("二进制内容")
	got, err := extractBinary(bytes.NewReader(tarGz(t, "aladdin", binary)))
	if err != nil {
		t.Fatalf("解包失败: %v", err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("解出来的内容不对：%q", got)
	}
}

func TestExtractBinaryRejectsUnexpectedArchives(t *testing.T) {
	// 带路径的条目：一个能写出 ../ 的包不该被解到任意位置。
	fromPath := tarGzNamed(t, "../aladdin", []byte("坏"))
	if _, err := extractBinary(bytes.NewReader(fromPath)); err == nil {
		t.Error("带路径的条目应当被拒绝")
	}

	// 空包。
	if _, err := extractBinary(bytes.NewReader(tarGzNamed(t, "", nil))); err == nil {
		t.Error("空包应当被拒绝")
	}

	// 不是 gzip。
	if _, err := extractBinary(strings.NewReader("这不是 gzip")); err == nil {
		t.Error("非 gzip 内容应当被拒绝")
	}
}

// tarGzNamed 造一个只含单个条目的包，名字由调用方指定（用于构造异常包）。
func tarGzNamed(t *testing.T, name string, content []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("写 tar 头失败: %v", err)
	}
	if len(content) > 0 {
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("写 tar 内容失败: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
	return buf.Bytes()
}

// 校验和清单只认发布流水线产出的那一种形态。
func TestParseChecksums(t *testing.T) {
	sums := parseChecksums([]byte(
		"aaaa  aladdin_v1.0.0_linux_amd64.tar.gz\n" +
			"bbbb  aladdin-web_v1.0.0.tar.gz\r\n" +
			"\n" +
			"这行不是清单\n" +
			"cccc\t单空格或制表符不算\n"))

	if got := sums["aladdin_v1.0.0_linux_amd64.tar.gz"]; got != "aaaa" {
		t.Errorf("第一行没解析出来：%q", got)
	}
	if got := sums["aladdin-web_v1.0.0.tar.gz"]; got != "bbbb" {
		t.Errorf("带 \\r 的行没解析出来：%q", got)
	}
	if len(sums) != 2 {
		t.Errorf("只应解析出两行，实际 %d 行：%v", len(sums), sums)
	}
}
