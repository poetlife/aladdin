package skill

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/relpath"
)

// 本文件是**一个技能包是什么**的唯一实现：从远端取回的那棵文件树，要满足哪些
// 条件才算一个包，以及怎样把校验过的内容变成一份清单与一组内容对象。
//
// 契约见 docs/design/skill/onboarding.md。三条取向值得在这里重述：
//
//   - **判据按内容判，不按扩展名判**："这些字节是不是文本"由字节本身回答（合法
//     UTF-8 且不含 NUL），而不是由一张扩展名白名单回答。后者要回答的是"下发什么
//     类型"，那是另一件事；而按"像不像会被执行"判会得到一条自相矛盾的规则——
//     一份写着"运行 build.sh"的 README 与那个 build.sh 本身没有理由区别对待。
//   - **上限都是常量**（文件数、单文件、总量），与任何部署形态无关。
//   - **校验失败不留任何痕迹**：本文件的入口不碰库、不碰对象存储，它只回答
//     "这棵树合不合法"。落地那一步在校验通过之后才开始（见 catalog.go）。

// contentObjectKeyPrefix 是技能内容对象在桶上的前缀。
//
// 它是**常量，不是配置项**：做成配置项只会多出一种失败方式——改了前缀，存量对象
// 在一瞬间全部变成孤儿。它与头像的 `avatars/`、galaxy 的 `galaxy/` 在同一个桶里
// 并存，三者互不干扰。
const contentObjectKeyPrefix = "skills/text/"

// ContentObjectKey 返回一份内容在私有区的对象键（唯一入口）。
//
// **键上不带技能标识、也不带版本标识**：内容寻址的键只回答"这份字节是什么"，
// 不回答"它属于谁、给谁用"——同一个 LICENSE 出现在十个技能里就是一个对象。代价
// 是**跨技能共享**：删除一个技能不能顺着键去删字节（那会删掉别处还在用的），
// 因此删除只删行。
func ContentObjectKey(digest string) string { return contentObjectKeyPrefix + digest }

// FetchedFile 是远端取回的一条文件。
//
// **它是字节，不是路径**：远端实现把内容读进内存交出来，全程不落盘（见
// docs/design/skill/onboarding.md 的"取回"）。包的上限（MaxPackageBytes）就是
// 为了给"最坏情况下这里有多少字节"一个界。
type FetchedFile struct {
	Path string
	Data []byte
}

// PackageFile 是校验通过之后的一条文件。
type PackageFile struct {
	Path   string
	Data   []byte
	Digest string
}

// SizeBytes 返回这一条的字节数。
func (f PackageFile) SizeBytes() int64 { return int64(len(f.Data)) }

// Package 是一棵**已校验**的技能包。
type Package struct {
	// Manifest 是 SKILL.md 的 frontmatter 里被读出来的两项。
	Manifest Manifest
	// Files 是按路径排序之后的文件（顺序固定，好让同样的输入得到同样的清单与
	// 留痕）。
	Files []PackageFile
}

// BuildPackage 校验一棵取回的树并把它变成一份包（唯一入口）。
//
// 它是**整套一次性**的：任何一条不满足即拒绝，且错误点名那一处（哪一个路径、
// 违反了哪一条）。调用方据此保证"被拒的纳管不留痕迹"。
func BuildPackage(files []FetchedFile) (Package, error) {
	ordered, err := orderFiles(files)
	if err != nil {
		return Package{}, err
	}
	if len(ordered) > MaxFiles {
		return Package{}, fmt.Errorf("%w: 文件数 %d 超过上限 %d",
			ErrPackageInvalid, len(ordered), MaxFiles)
	}

	var total int64
	packed := make([]PackageFile, 0, len(ordered))
	var manifestData []byte
	for _, file := range ordered {
		if err := validatePackageFile(file); err != nil {
			return Package{}, err
		}
		total += int64(len(file.Data))
		if total > MaxPackageBytes {
			return Package{}, fmt.Errorf("%w: 字节总数超过上限 %d",
				ErrPackageInvalid, MaxPackageBytes)
		}
		if file.Path == ManifestPath {
			manifestData = file.Data
		}
		packed = append(packed, PackageFile{
			Path:   file.Path,
			Data:   file.Data,
			Digest: objectstore.ContentDigest(file.Data),
		})
	}
	// 清单文件是包契约里唯一"必须有"的一条，因此它缺失时要单独说清楚——只说
	// "包不合法"会让管理员在一堆文件里找不出该补哪一个。
	if manifestData == nil {
		return Package{}, fmt.Errorf("%w: 包根缺少 %s", ErrPackageInvalid, ManifestPath)
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		return Package{}, err
	}
	return Package{Manifest: manifest, Files: packed}, nil
}

// orderFiles 按路径排序并挡掉重复路径（唯一入口）。
//
// 两种重复都要挡：
//
//   - **完全相同**的路径是同一棵树里出现了两条同名条目，而清单是一张「路径 →
//     摘要」的表，留哪一条都没有依据；
//   - **只差大小写**的路径在大小写不敏感的文件系统上是同一个文件，而包会被取到
//     那样的文件系统上。挡在这里，而不是在下发时——下发是读出清单，它不该有一
//     个"这两条其实是同一条"的分支。
func orderFiles(files []FetchedFile) ([]FetchedFile, error) {
	ordered := slices.Clone(files)
	slices.SortFunc(ordered, func(a, b FetchedFile) int { return strings.Compare(a.Path, b.Path) })

	seen := make(map[string]string, len(ordered))
	folded := make(map[string]string, len(ordered))
	for _, file := range ordered {
		if file.Path == "" {
			return nil, fmt.Errorf("%w: 有一条条目没有路径", ErrPackageInvalid)
		}
		if previous, dup := seen[file.Path]; dup {
			return nil, fmt.Errorf("%w: 路径 %q 出现了两次", ErrPackageInvalid, previous)
		}
		seen[file.Path] = file.Path

		lower := strings.ToLower(file.Path)
		if previous, dup := folded[lower]; dup {
			return nil, fmt.Errorf("%w: 路径 %q 与 %q 只差大小写，在大小写不敏感的机器上是同一个文件",
				ErrPackageInvalid, previous, file.Path)
		}
		folded[lower] = file.Path
	}
	return ordered, nil
}

// validatePackageFile 校验一条文件（唯一入口）。
func validatePackageFile(file FetchedFile) error {
	if !relpath.Valid(file.Path) {
		return fmt.Errorf("%w: 路径 %q 形状不合法（相对路径、只含 URL 非保留字符、不含点段）",
			ErrPackageInvalid, file.Path)
	}
	if len(file.Data) > MaxFileBytes {
		return fmt.Errorf("%w: %q 有 %d 字节，超过单文件上限 %d",
			ErrPackageInvalid, file.Path, len(file.Data), MaxFileBytes)
	}
	if !isText(file.Data) {
		return fmt.Errorf("%w: %q 不是文本（含 NUL 字节或不是合法 UTF-8）",
			ErrPackageInvalid, file.Path)
	}
	return nil
}

// isText 判定一段字节能不能当文本处理（唯一入口）。
//
// 两条判据合起来就够了：合法 UTF-8，且不含 NUL。**它不按扩展名判**：平台分发的是
// 能被读的字节，而脚本类文本同样能被读——收不收得下由"它是不是文本"决定，不由
// "它像不像会被执行"决定，因为平台从不执行它（见 docs/design/skill/README.md 的
// 模块边界）。
func isText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	return !strings.ContainsRune(string(data), 0)
}

// manifestFrontmatter 是 SKILL.md 开头那段 frontmatter 的解析目标。
//
// 只声明平台读的两项。**其余字段不声明也不校验**：它们属于这个技能的读者，
// 不属于平台（见 docs/design/skill/onboarding.md）。
type manifestFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ParseManifest 解析 SKILL.md 的 frontmatter（唯一入口）。
//
// `name` 与 `description` 都必填：前者是英文检索与回退展示名，后者是**触发说明**
// ——"这个技能什么时候该被用上"的唯一答案，而读者（人或 agent）正是靠它决定要
// 不要用。
func ParseManifest(data []byte) (Manifest, error) {
	block, err := frontmatterBlock(data)
	if err != nil {
		return Manifest{}, err
	}
	var parsed manifestFrontmatter
	if err := yaml.Unmarshal([]byte(block), &parsed); err != nil {
		return Manifest{}, fmt.Errorf("%w: %s 的 frontmatter 不是合法 YAML: %w",
			ErrPackageInvalid, ManifestPath, err)
	}

	name := strings.TrimSpace(parsed.Name)
	if name == "" {
		return Manifest{}, fmt.Errorf("%w: %s 的 frontmatter 缺少 name", ErrPackageInvalid, ManifestPath)
	}
	if err := validateSkillName(name); err != nil {
		return Manifest{}, err
	}
	description := strings.TrimSpace(parsed.Description)
	if description == "" {
		return Manifest{}, fmt.Errorf("%w: %s 的 frontmatter 缺少 description（触发说明）",
			ErrPackageInvalid, ManifestPath)
	}
	if len([]rune(description)) > MaxDescriptionRunes {
		return Manifest{}, fmt.Errorf("%w: %s 的 description 超过 %d 个字",
			ErrPackageInvalid, ManifestPath, MaxDescriptionRunes)
	}
	return Manifest{Name: name, Description: description}, nil
}

// frontmatterBlock 取出一份文件开头那段 `---` 之间的内容。
//
// 首行必须是 `---`：**没有"整份文件都是 frontmatter"这种退化读法**——那会让一份
// 普通的、恰好是合法 YAML 的 markdown 被当成清单解析，而它的 `description` 是
// 什么完全取决于它写了什么。
func frontmatterBlock(data []byte) (string, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") && text != "---" {
		return "", fmt.Errorf("%w: %s 缺少开头的 frontmatter（首行应为 ---）",
			ErrPackageInvalid, ManifestPath)
	}
	rest := strings.TrimPrefix(text, "---\n")
	if newline := strings.Index(rest, "\n---"); newline >= 0 {
		return rest[:newline], nil
	}
	if strings.HasPrefix(rest, "---") {
		return "", nil
	}
	return "", fmt.Errorf("%w: %s 的 frontmatter 没有结束标记（应为单独一行的 ---）",
		ErrPackageInvalid, ManifestPath)
}

// validateSkillName 校验 frontmatter 里的 name。
//
// 取值收在小写字母、数字与连字符上：它是这个技能在**英文语境**里的标识，也是
// 别的工具引用它时用的名字。宽一点点只会让同一个技能出现两种看起来都对的写法。
func validateSkillName(name string) error {
	if len([]rune(name)) > MaxNameRunes {
		return fmt.Errorf("%w: %s 的 name 超过 %d 个字符",
			ErrPackageInvalid, ManifestPath, MaxNameRunes)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("%w: %s 的 name %q 只能用小写字母、数字与连字符",
				ErrPackageInvalid, ManifestPath, name)
		}
	}
	return nil
}
