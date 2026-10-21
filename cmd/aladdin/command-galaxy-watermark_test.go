package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/internal/watermark"
)

// 水印提取那一条命令：**一张图进，一次访问出**。
//
// 这里造图的方式与端到端测试同一条思路——用服务端**真正下发**的那份图案铺一张
// "截图"，再从里面解。它证明的是命令行这一侧把两件事接上了：解图（算法入口）与
// 按码查登记（服务端入口）。用一张手画的图就测不到"下发的图案真的解得开"。

// fakeWatermark 是一台只回答查水印的假服务端。
type fakeWatermark struct {
	galaxyv1connect.UnimplementedGalaxyServiceHandler
	visit *galaxyv1.WatermarkRecord
}

func (f *fakeWatermark) LookupWatermark(_ context.Context, req *connect.Request[galaxyv1.LookupWatermarkRequest]) (*connect.Response[galaxyv1.LookupWatermarkResponse], error) {
	// 码不对时**空结果而不是错误**：那是服务端的否定结论（见
	// docs/design/galaxy/watermark.md），命令行据此给出同一条"查不到"。
	if f.visit == nil || req.Msg.GetCode() != f.visit.GetCode() {
		return connect.NewResponse(&galaxyv1.LookupWatermarkResponse{}), nil
	}
	return connect.NewResponse(&galaxyv1.LookupWatermarkResponse{Visit: f.visit}), nil
}

// screenshotOf 用服务端下发的图案铺一张"截图"并写到临时目录，返回它的路径。
//
// 铺法照着壳上那一层：图案按原尺寸平铺。**裁掉左上角一块**是刻意的——真实截图
// 不会正好对齐帧原点，提取必须自己把相位找回来。
func screenshotOf(t *testing.T, code string) string {
	t.Helper()
	pattern, err := watermark.Tile(code)
	if err != nil {
		t.Fatalf("生成图案失败: %v", err)
	}
	tile, err := png.Decode(bytes.NewReader(pattern))
	if err != nil {
		t.Fatalf("图案不是 PNG: %v", err)
	}
	bounds := tile.Bounds()
	shot := image.NewRGBA(image.Rect(0, 0, 3*bounds.Dx(), 2*bounds.Dy()))
	for y := 0; y < shot.Bounds().Dy(); y++ {
		for x := 0; x < shot.Bounds().Dx(); x++ {
			shot.Set(x, y, tile.At(bounds.Min.X+x%bounds.Dx(), bounds.Min.Y+y%bounds.Dy()))
		}
	}
	cropped := shot.SubImage(image.Rect(31, 23, shot.Bounds().Dx(), shot.Bounds().Dy()))

	path := filepath.Join(t.TempDir(), "截图.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("建临时文件失败: %v", err)
	}
	defer func() { _ = file.Close() }()
	if err := png.Encode(file, cropped); err != nil {
		t.Fatalf("写截图失败: %v", err)
	}
	return path
}

// runWatermarkCLI 在进程内跑一条水印命令。
func runWatermarkCLI(t *testing.T, fake *fakeWatermark, args ...string) (string, error) {
	t.Helper()
	_, handler := galaxyv1connect.NewGalaxyServiceHandler(fake)
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	server := strings.TrimPrefix(api.URL, "http://")

	root := newRootCommand()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs(append([]string{"--address", server, "--token", "test-token", "--yes"}, args...))
	err := root.Execute()
	return stdout.String(), err
}

// **解出码，并把它对回一次访问。**
//
// 这一条把两端接在一起：解图走算法入口（internal/watermark），按码查走服务端入口
// （LookupWatermark）。任一端对不上，这条命令输出的就是一句空话。
func TestWatermarkExtractLooksUpTheVisit(t *testing.T) {
	const code = "0123456789ABCDEF"
	fake := &fakeWatermark{visit: &galaxyv1.WatermarkRecord{
		Code:            code,
		SubjectId:       "usr_reader",
		VisitedAt:       "2026-10-18T12:00:00Z",
		ProjectId:       "prj_example",
		Slot:            galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
		SourceAddress:   "203.0.113.9",
		UserAgentDigest: "0123456789abcdef",
	}}
	shot := screenshotOf(t, code)

	out, err := runWatermarkCLI(t, fake, "galaxy", "watermark", "extract", shot)
	if err != nil {
		t.Fatalf("extract 失败: %v\n%s", err, out)
	}
	for _, want := range []string{code, "usr_reader", "prj_example", "203.0.113.9", "2026-10-18"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里没有 %q:\n%s", want, out)
		}
	}
}

// **--code-only 只解不查**：取证的第一步"这张图上有没有码"不需要任何权限，也不
// 必连得上服务端。这里给一台会拒绝一切的假服务端，命令仍该成功。
func TestWatermarkExtractCodeOnlyDoesNotCallServer(t *testing.T) {
	const code = "ZYXWVTSRQPNMKJH0"
	fake := &fakeWatermark{}
	shot := screenshotOf(t, code)

	out, err := runWatermarkCLI(t, fake, "galaxy", "watermark", "extract", shot, "--code-only")
	if err != nil {
		t.Fatalf("extract --code-only 失败: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != code {
		t.Fatalf("输出 = %q，期望恰好是码 %q", strings.TrimSpace(out), code)
	}
}

// **查不到与解不出是两句话，但都不是"成功"。**
//
// 前者是"图里有码、但这枚码对不回人"（超出保留期，或记录已被清掉）；后者是
// "这张图里根本没有本平台的码"。两条都以非零状态结束——脚本据此分支。
func TestWatermarkExtractFailsWhenNoVisitOrNoCode(t *testing.T) {
	const code = "0123456789ABCDEF"
	shot := screenshotOf(t, code)

	// 图里有码，但服务端查不到。
	out, err := runWatermarkCLI(t, &fakeWatermark{}, "galaxy", "watermark", "extract", shot)
	if err == nil {
		t.Fatalf("查不到记录时应当以非零状态结束，实际成功:\n%s", out)
	}
	if !strings.Contains(err.Error(), "查不到") {
		t.Errorf("错误里没有说清是「查不到」：%v", err)
	}

	// 图里没有码（一张纯白）。
	blank := image.NewRGBA(image.Rect(0, 0, 900, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 900; x++ {
			blank.Set(x, y, image.White)
		}
	}
	path := filepath.Join(t.TempDir(), "空白.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("建临时文件失败: %v", err)
	}
	if encodeErr := png.Encode(file, blank); encodeErr != nil {
		t.Fatalf("写空白图失败: %v", encodeErr)
	}
	_ = file.Close()

	_, err = runWatermarkCLI(t, &fakeWatermark{}, "galaxy", "watermark", "extract", path, "--code-only")
	if err == nil {
		t.Fatal("图里没有码时应当以非零状态结束")
	}
	if !strings.Contains(err.Error(), "解不出") {
		t.Errorf("错误里没有说清是「解不出」：%v", err)
	}
}
