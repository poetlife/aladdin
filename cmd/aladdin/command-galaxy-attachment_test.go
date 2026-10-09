package main

import (
	"errors"
	"path/filepath"
	"testing"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/objectstore"
)

// 附件的上传声明**固定为中性的那一档**，而它必须与服务端签发时写进策略的类型
// 逐字相同：差一个字符，存储侧的类型条件就对不上，表现是一次"直传被拒"。
//
// 这里不重写那个取值，而是断言命令行用的是**同一个常量**。
func TestAttachmentUploadDeclaresNeutralType(t *testing.T) {
	if objectstore.NeutralContentType != "application/octet-stream" {
		t.Fatalf("中性类型变了：%q——附件下发时的内容类型由服务端固定成它，"+
			"命令行的直传声明必须跟着走", objectstore.NeutralContentType)
	}
}

// 缺省文件名取原始文件名的最后一段：库里存的是**上传时原样记下的名字**，
// 它可能含路径分隔符，也可能只是一个指向目录的取值。
func TestDefaultDownloadName(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		want     string
	}{
		{"普通文件名原样", "build.zip", "build.zip"},
		{"带目录的名字只取最后一段", "dist/pkg/build.zip", "build.zip"},
		{"上跳的路径不落到目录之外", "../../etc/passwd", "passwd"},
		{"空的回退到一个明确的名字", "", "attachment"},
		{"只有点回退到明确的名字", ".", "attachment"},
		{"只有两个点同样回退", "..", "attachment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultDownloadName(tc.filename); got != tc.want {
				t.Errorf("defaultDownloadName(%q) = %q，期望 %q", tc.filename, got, tc.want)
			}
		})
	}
}

// 下载的输出路径会落在当前目录之外吗？不会：缺省名只取最后一段，且挡掉了那几个
// 指向目录本身的取值。
func TestDefaultDownloadNameStaysInDirectory(t *testing.T) {
	for _, filename := range []string{"..", "../..", "/", `..\..\x`} {
		name := defaultDownloadName(filename)
		if name != filepath.Base(name) || name == ".." || name == "." {
			t.Errorf("defaultDownloadName(%q) = %q，不是一个可以落在当前目录里的名字",
				filename, name)
		}
	}
}

// 附件的显示名就是文件名（附件没有展示标题那一层），空时给一句说明而不是空串。
func TestAttachmentLabel(t *testing.T) {
	if got := attachmentLabel(&galaxyv1.Attachment{Filename: "build.zip"}); got != "build.zip" {
		t.Errorf("attachmentLabel = %q，期望 build.zip", got)
	}
	if got := attachmentLabel(&galaxyv1.Attachment{}); got != "(未命名)" {
		t.Errorf("attachmentLabel = %q，期望一句说明", got)
	}
}

// attachment update 必须给出 --description：不给标志是一次没有任何改动的调用，
// **在发起请求之前**就该被拦下（与 asset update 同一条取向）。
func TestAttachmentUpdateRequiresDescription(t *testing.T) {
	err := executeRoot(t, "galaxy", "attachment", "update", "prj_x", "atc_y")
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("不给 --description 时必须是用法错误，实际：%v", err)
	}
}

// 从清单里按标识找附件：找得到给那一条，找不到给 nil。
func TestFindAttachment(t *testing.T) {
	attachments := []*galaxyv1.Attachment{{Id: "atc_1"}, {Id: "atc_2"}}
	if got := findAttachment(attachments, "atc_2"); got == nil || got.GetId() != "atc_2" {
		t.Errorf("findAttachment = %v，期望 atc_2", got)
	}
	if got := findAttachment(attachments, "atc_9"); got != nil {
		t.Errorf("findAttachment 找到了不存在的附件: %v", got)
	}
}

// download 的位置参数是 2 到 3 个：目标路径可省。给多了要在发起请求之前结束。
func TestAttachmentDownloadArgCount(t *testing.T) {
	err := executeRoot(t, "galaxy", "attachment", "download", "prj_x", "atc_y", "a", "b")
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("参数过多时必须是用法错误，实际：%v", err)
	}
}
