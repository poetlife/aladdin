package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/objectstore"
)

// fileSetFile 是本地目录里的一份文件：它的**文件组路径**与它的字节。
type fileSetFile struct {
	// Path 是它在文件组里的相对路径（以 `/` 分隔，与目录层级一致）。
	Path string
	// Data 是整份字节。文件组的体积上限在 MiB 量级，一次读进内存是够用的，
	// 而摘要要对着整份字节算。
	Data []byte
}

// readFileSet 读一个本地目录，把它变成一组具名文件（唯一入口）。
//
// **目录是整组的输入单位**：文件组的形状是一组具名文件，因此命令行上与它对应
// 的也是目录，而不是一段文本（见 docs/design/galaxy/cli.md）。
//
// 三条规则，都刻意选"说出来"而不是"猜过去"：
//
//   - **隐藏文件被跳过**（以 `.` 开头）。构建产物的目录里常有 `.DS_Store`、
//     `.gitignore` 这类东西，它们既不是站点的一部分，也不是用户想发布的内容。
//   - **符号链接被拒**。跟随它会让"这个目录里有什么"取决于链接指向哪里，而
//     目录是整组的输入单位——一次 pull 回来的目录里不该出现一个指向别处的项。
//   - **保留段被拒**（`docs` 由文档槽占用，见 galaxy.ValidateSlotPath）。这条
//     在**发送之前**就报错，省一次往返，也让错误在人还记得自己刚动过什么的时候
//     出现。
//   - **白名单外的扩展名被拒**，而不是被静默丢掉。判定"是不是文本"用的是**服务端
//     那张白名单**（galaxy.IsTextPath），因此"命令行以为这是文本、服务端拒了它"
//     不会发生；剩下的必须是资产类型表认识的，否则用户会得到一句明确的话，而不
//     是一个静默少掉的文件。
func readFileSet(dir string, slot galaxy.ContentSlot) ([]fileSetFile, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, usageErrorf("读取目录 %s 失败：%v", dir, err)
	}
	if !info.IsDir() {
		return nil, usageErrorf("%s 不是一个目录：文件组的输入单位是目录", dir)
	}

	var files []fileSetFile
	var total int
	err = filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		if relative == "." {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return usageErrorf("%s 是一个符号链接：目录是整组的输入单位，不跟随链接", p)
		}
		if entry.IsDir() {
			return nil
		}
		// 文件组里的路径一律以 `/` 分隔，与操作系统无关。
		entryPath := filepath.ToSlash(relative)
		if !galaxy.ValidEntryPath(entryPath) {
			return usageErrorf("路径 %q 不合形状（见 docs/design/galaxy/site-model.md 的路径约束）", entryPath)
		}
		if err := galaxy.ValidateSlotPath(slot, entryPath); err != nil {
			return usageErrorf("%q 落在保留段里：%s 这一段由文档槽占用，站点槽的路径不能以它开头",
				entryPath, galaxy.ReservedSegment)
		}
		isText := galaxy.IsTextPath(slot, entryPath)
		if !isText {
			if _, ok := assetTypeForPath(entryPath); !ok {
				return usageErrorf("%q 既不是 %s 槽的文本（%s），也不在资产类型表里；"+
					"把它删掉，或换一个认识的扩展名",
					entryPath, slot, strings.Join(textExtensions(slot), "、"))
			}
		}
		//nolint:gosec // 路径来自调用者自己的命令行参数，读的是他自己的文件
		data, err := os.ReadFile(p)
		if err != nil {
			return usageErrorf("读取 %s 失败：%v", p, err)
		}
		if isText && len(data) > galaxy.MaxTextBytes {
			return usageErrorf("%s 有 %d 字节，超过单份文本上限 %d 字节",
				entryPath, len(data), galaxy.MaxTextBytes)
		}
		total += len(data)
		files = append(files, fileSetFile{Path: entryPath, Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, usageErrorf("目录 %s 里没有任何可以发布的文件", dir)
	}
	if len(files) > galaxy.MaxFiles {
		return nil, usageErrorf("目录里有 %d 个文件，超过上限 %d 个", len(files), galaxy.MaxFiles)
	}
	if total > galaxy.MaxFileSetBytes {
		return nil, usageErrorf("目录里的内容共 %d 字节，超过整组上限 %d 字节", total, galaxy.MaxFileSetBytes)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// writeFileSet 把一组条目写到本地目录（唯一入口）。
//
// fetch 给出一个条目的字节：文本条目与资产条目都从服务端下发的**短时地址**
// 直连取，服务端不代理字节。
func writeFileSet(dir string, entries []*galaxyv1.FileEntry, fetch func(*galaxyv1.FileEntry) ([]byte, error)) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return usageErrorf("创建目录 %s 失败：%v", dir, err)
	}
	prefix := strings.TrimSuffix(filepath.Clean(dir), string(filepath.Separator)) + string(filepath.Separator)
	for _, entry := range entries {
		target := filepath.Join(dir, filepath.FromSlash(entry.GetPath()))
		// 条目路径已经过了形状约束（不含 `..`、不以 `/` 开头），因此它一定落在
		// dir 之内；这里再确认一次，因为写文件是不可逆的。
		if !strings.HasPrefix(target, prefix) {
			return usageErrorf("路径 %q 落到目录之外", entry.GetPath())
		}
		data, err := fetch(entry)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return usageErrorf("创建目录失败：%v", err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return usageErrorf("写入 %s 失败：%v", target, err)
		}
	}
	return nil
}

// textExtensions 返回一个内容槽下的文本扩展名，供报错信息使用。
//
// 它**只为把可选项念给用户听**，不参与任何判定——判定一律走
// galaxy.IsTextPath（服务端那一个白名单）。
func textExtensions(slot galaxy.ContentSlot) []string {
	ordered := map[galaxy.ContentSlot][]string{
		galaxy.SlotSite: {".html", ".htm", ".css", ".js", ".mjs", ".json", ".txt", ".svg"},
		galaxy.SlotDocs: {".md", ".css", ".js", ".mjs", ".json"},
	}
	return ordered[slot]
}

// describeEntry 输出一条条目的一行摘要。
func describeEntry(entry *galaxyv1.FileEntry) string {
	if entry.GetAssetId() != "" {
		return fmt.Sprintf("%s  资产 %s", entry.GetPath(), entry.GetAssetId())
	}
	return fmt.Sprintf("%s  文本 %s", entry.GetPath(), entry.GetDigest())
}

// entriesSignature 把一组条目压成一个可比较的签名（路径 + 字节从哪来）。
//
// 它回答的是"这两组是不是同一份内容"，用在 push 之后的引导上：手里的这一组是
// "文件 + 记号登记"两步拼出来的，而服务端存下来的是排好序的清单——**顺序不参与**，
// 因此这里统一按路径排序再比。两处各排一次序就会漂，而漂的表现是"明明没改，却被
// 提示尚未存为版本"。
func entriesSignature(entries []*galaxyv1.FileEntry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		// 两块拼在一起就够区分文本条目与资产条目：同一个 oneof 的两个分支最多只有
		// 一个非空。
		parts = append(parts, entry.GetPath()+"\x00"+entry.GetDigest()+entry.GetAssetId())
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

// uploadFileSet 把一组本地文件送成文件组的条目（唯一入口）。
//
// 两条路，与文件组的两类条目一一对应：
//
//   - **文本文件**走内容对象：按内容摘要、**仅当不存在时写入**。因此一份没改过
//     的文件一次上传都不用做——服务端说它已经在了，就直接引用；
//   - **其余文件**走资产上传：分配标识、声明类型、直传、提交。
//
// 两者走的是同一条直传链路（签发 → 直传 → 提交），因此这里是"整组送上去"的
// 编排，而它仍然是一次编排、不是多次状态跃迁。
//
// 最后一步是**记号登记**：文本里出现 `asset://<资产标识>` 的地方要额外成为一条
// 资产条目，否则发布时那处引用落在不了文件组里。
func uploadFileSet(ctx context.Context, svc galaxyv1connect.GalaxyServiceClient, projectID string, slot galaxy.ContentSlot, files []fileSetFile) ([]*galaxyv1.FileEntry, error) {
	entries := make([]*galaxyv1.FileEntry, 0, len(files))
	usedPaths := make(map[string]bool, len(files))

	for _, file := range files {
		if galaxy.IsTextPath(slot, file.Path) {
			entry, err := uploadTextFile(ctx, svc, projectID, file)
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
			usedPaths[file.Path] = true
			continue
		}
		entry, err := uploadAssetFile(ctx, svc, projectID, file)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
		usedPaths[file.Path] = true
	}

	// 记号登记：源侧的 asset:// 在**进入文件组**这一步被解析成条目，此后服务端
	// 只认文件组（见 docs/design/galaxy/authoring.md）。
	for _, file := range files {
		if !galaxy.IsTextPath(slot, file.Path) {
			continue
		}
		for _, assetID := range galaxy.AssetMarkerIDs(file.Data) {
			markerPath := galaxy.AssetMarkerPath(assetID)
			if usedPaths[markerPath] {
				continue
			}
			usedPaths[markerPath] = true
			entries = append(entries, &galaxyv1.FileEntry{
				Path:   markerPath,
				Source: &galaxyv1.FileEntry_AssetId{AssetId: assetID},
			})
		}
	}
	return entries, nil
}

// uploadTextFile 把一份文本送成内容对象，并返回它的条目。
func uploadTextFile(ctx context.Context, svc galaxyv1connect.GalaxyServiceClient, projectID string, file fileSetFile) (*galaxyv1.FileEntry, error) {
	digest := galaxy.ContentDigest(file.Data)
	// 摘要由命令行的**同一个摘要入口**算，不在这里另写一份：浏览器与服务端
	// 共用同一个算法，命令行也必须只想一份实现。
	begin, err := svc.BeginContentUpload(ctx, connect.NewRequest(&galaxyv1.BeginContentUploadRequest{
		ProjectId: projectID,
		Digest:    digest,
		SizeBytes: uint64(len(file.Data)),
	}))
	if err != nil {
		return nil, err
	}
	if !begin.Msg.GetAlreadyExists() {
		// 中性类型：文本的下发类型由**路径**派生，对象本身不承担类型语义
		// （见 internal/objectstore 的 NeutralContentType）。
		if err := directUpload(begin.Msg.GetUpload(), file.Data, objectstore.NeutralContentType); err != nil {
			return nil, err
		}
		if _, err := svc.CommitContentUpload(ctx, connect.NewRequest(&galaxyv1.CommitContentUploadRequest{
			ProjectId: projectID,
			Digest:    digest,
		})); err != nil {
			return nil, err
		}
	}
	return &galaxyv1.FileEntry{
		Path:   file.Path,
		Source: &galaxyv1.FileEntry_Digest{Digest: digest},
	}, nil
}

// uploadAssetFile 把一份非文本文件送成资产，并返回它的条目。
func uploadAssetFile(ctx context.Context, svc galaxyv1connect.GalaxyServiceClient, projectID string, file fileSetFile) (*galaxyv1.FileEntry, error) {
	mediaType, ok := assetTypeForPath(file.Path)
	if !ok {
		// readFileSet 已经挡过一次；走到这里说明调用方绕过了它。
		return nil, usageErrorf("%q 不在资产类型表里", file.Path)
	}
	begin, err := svc.BeginAssetUpload(ctx, connect.NewRequest(&galaxyv1.BeginAssetUploadRequest{
		ProjectId:   projectID,
		ContentType: mediaType,
		SizeBytes:   uint64(len(file.Data)),
	}))
	if err != nil {
		return nil, err
	}
	if err := directUpload(begin.Msg.GetUpload(), file.Data, mediaType); err != nil {
		return nil, err
	}
	commit, err := svc.CommitAssetUpload(ctx, connect.NewRequest(&galaxyv1.CommitAssetUploadRequest{
		ProjectId:   projectID,
		AssetId:     begin.Msg.GetAssetId(),
		ContentType: mediaType,
		Digest:      galaxy.ContentDigest(file.Data),
		Filename:    filepath.Base(file.Path),
	}))
	if err != nil {
		return nil, err
	}
	return &galaxyv1.FileEntry{
		Path:   file.Path,
		Source: &galaxyv1.FileEntry_AssetId{AssetId: commit.Msg.GetAsset().GetId()},
	}, nil
}

// fetchEntry 按条目的短时地址直连取字节。
//
// **服务端不代理字节**：清单里下发的是短时预签名地址，取字节这一步由客户端
// 直接对对象存储发起（见 docs/design/galaxy/asset-library.md）。
func fetchEntry(entry *galaxyv1.FileEntry) ([]byte, error) {
	if entry.GetUrl() == "" {
		return nil, fmt.Errorf("%s 没有可用的读取地址（可能是对象存储没有配置，或地址签发失败）", entry.GetPath())
	}
	return downloadBytes(entry.GetUrl())
}

// downloadBytes 取一份短时地址上的字节。
func downloadBytes(rawURL string) ([]byte, error) {
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(rawURL) //nolint:gosec,noctx // 地址来自服务端下发的预签名地址，不是用户输入
	if err != nil {
		return nil, fmt.Errorf("下载失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("下载失败：%w", err)
	}
	return data, nil
}

// downloadTimeout 是一次下载的超时。与直传同一个尺度：它搬的可能是几十 MB 的
// 视频，而 --timeout 是单次 RPC 的尺度。
const downloadTimeout = 30 * time.Minute
