package upgrade

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrNotReleased 表示本机二进制不是由发布产物安装的（本地构建、go install）。
//
// 它是一条**拒绝**，不是一个可以绕过的检查：调用方据此给出"当前是什么版本、
// 为什么不能自更新、该怎么做"的说明，且**不发起任何网络请求**。
var ErrNotReleased = errors.New("本机二进制不是发布产物")

// Version 是一个严格形态的版本号：vX.Y.Z。
//
// 严格是刻意的。它是区分"这份二进制来自发布产物"与"这份二进制是本机自产的"
// 最省事、也最难伪造的判据：发布流水线由 tag 注入版本，而 tag 的形态受
// release.yml 的 validate 约束；本机构建得到的是 dev 或 git describe 的形态
// （v0.4.3-5-gabc1234-dirty），两者都解析不了。
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion 解析严格形态 vX.Y.Z 的版本。
//
// 不接受预发布后缀（v1.0.0-rc.1）、构建元数据、或缺少前导 v 的形态——发布
// 流水线产出的版本号一定是这个形状，其它形状一律按"本机构建"处理。
func ParseVersion(s string) (Version, bool) {
	// 构建期注入的取值可能带上换行（Makefile 变量拼进 -ldflags 时），
	// 去掉它不影响严格性：它变不出一个发布产物。
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "v") {
		return Version{}, false
	}

	parts := strings.Split(s[1:], ".")
	if len(parts) != 3 {
		return Version{}, false
	}

	var nums [3]int
	for i, part := range parts {
		// 拒绝空段、前导零（"01"）、正负号与非数字：它们都能让同一个版本
		// 有不止一种写法，而"同一个版本只有一种写法"正是比较的前提。
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return Version{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}, true
}

// String 返回带前导 v 的规范写法。
func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare 比较两个版本：小于返回负、相等返回 0、大于返回正。
func (v Version) Compare(other Version) int {
	switch {
	case v.Major != other.Major:
		return v.Major - other.Major
	case v.Minor != other.Minor:
		return v.Minor - other.Minor
	default:
		return v.Patch - other.Patch
	}
}
