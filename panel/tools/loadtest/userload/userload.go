// [INPUT]: 占位
// [OUTPUT]: 对外提供 Main
// [POS]: tools/loadtest 的 userload 子命令（占位，待实现）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package userload

import "errors"

// Main 是 userload 子命令的入口（占位）。
func Main(args []string) error { return errors.New("userload: not implemented") }

// BurstMain 是 burst 子命令（占位，由 userload 的实现替换）。
func BurstMain(args []string) error { return errors.New("burst: not implemented") }
