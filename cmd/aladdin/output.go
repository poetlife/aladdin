package main

import (
	"fmt"
	"io"
)

// 写 stdout/stderr 失败（如下游管道提前关闭）在实践中无法补救，
// 但也不能因此让 errcheck 长期报错——真正的漏检会被淹没在噪声里。
//
// 因此这里把忽略**显式化**在唯一的一处，而不是在 .golangci.yml 里
// 关掉整个 cmd/ 的 errcheck：前者写明了意图，后者只是让检查闭嘴。

// printf 向 w 写入格式化文本，忽略写失败。
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// println 向 w 写入一行，忽略写失败。
func println(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}
