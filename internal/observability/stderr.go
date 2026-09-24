package observability

import "os"

// stderrWriteSyncer 把控制台日志固定写到 stderr。
//
// 服务端与 CLI 都走 stderr：CLI 的 stdout 可能被管道消费（如 --output json），
// 日志混入 stdout 会破坏机器可读输出。
type stderrWriteSyncer struct{}

// Write 实现 zapcore.WriteSyncer。
func (stderrWriteSyncer) Write(p []byte) (int, error) { return os.Stderr.Write(p) }

// Sync 实现 zapcore.WriteSyncer。
func (stderrWriteSyncer) Sync() error { return nil }
