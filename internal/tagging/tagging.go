// Package tagging 是**标签**这一层数据的唯一实现：一组自由字符串怎么被归一化成
// 可以逐字比较、可以跨存储实现与跨端一致的那一份。
//
// 它被两处消费：galaxy 的资产（说明层标签）与 skill 的技能（目录标签）。两处的
// 差别只有"标签挂在什么上"，而"一串标签归一化之后长什么样"必须**逐字相同**——
// 行为规则见 docs/design/galaxy/asset-library.md 的"可编辑元数据"与
// docs/design/skill/catalog.md 的"标签"。
//
// 它不是"工具包"：这里放的是**同一条规则**，不是若干相近的小函数。两处各写一份
// 的代价不是多几行代码，而是同一串标签在两处被归一化成不同的样子——那种问题
// 的表现是"这个标签看着对、就是筛不出来"。
package tagging

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

const (
	// MaxRunesPerTag 是单个标签的长度上限。
	//
	// 按**字符数**而不是字节数计：用户感知的长度是字数，按字节算会让"一段中文
	// 写到十几个字就被拒"成为一条需要解释的规则。
	MaxRunesPerTag = 32
	// MaxTags 是一个对象上的标签数量上限（按归一化去重之后的个数算）。
	MaxTags = 16
)

var (
	// ErrInvalid 表示某个标签不合法（空串、含控制字符或路径分隔符、超长）。
	ErrInvalid = errors.New("标签不合法")

	// ErrTooMany 表示标签数量超过上限。
	ErrTooMany = errors.New("标签数量超过上限")
)

// Normalize 归一化一组标签（唯一入口）。
//
// 规则：
//
//   - 去掉首尾空白；
//   - **统一小写**。存储与展示都用它，因此不存在"库里存一份、比较时另算一份"
//     这第二处规则；
//   - 空串、含控制字符、含 `/` 或 `\` 的取值被拒。前两者是"看不见的取值"，
//     后者是路径分隔符——标签会出现在查询串与界面里，收下它等于把转义问题
//     往后推；
//   - 归一化之后相同的只留一个，并按**字典序**排好。顺序在三端与两个存储实现
//     之间逐字一致，因此不存在"内存实现读回来的顺序和 SQL 不一样"这条只在
//     契约测试里才看得见的偏差；调用方也不必也不得再排一次。
//
// 它只归一化，不碰存储也不判权。
func Normalize(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case tag == "":
			return nil, fmt.Errorf("%w: 标签不能为空", ErrInvalid)
		case strings.ContainsAny(tag, `/\`):
			return nil, fmt.Errorf("%w: %q 含路径分隔符", ErrInvalid, tag)
		case ContainsControlRune(tag):
			return nil, fmt.Errorf("%w: %q 含控制字符", ErrInvalid, tag)
		case len([]rune(tag)) > MaxRunesPerTag:
			return nil, fmt.Errorf("%w: %q 超过 %d 个字", ErrInvalid, tag, MaxRunesPerTag)
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	if len(normalized) > MaxTags {
		return nil, fmt.Errorf("%w: 上限 %d 个", ErrTooMany, MaxTags)
	}
	slices.Sort(normalized)
	return normalized, nil
}

// ContainsControlRune 判定一段文本里有没有控制字符。
//
// 导出是因为它不止被标签用：skill 的包校验也拿它判"这份字节是不是可以当文本
// 处理"，两处判的必须是同一件事。
func ContainsControlRune(text string) bool {
	for _, r := range text {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
