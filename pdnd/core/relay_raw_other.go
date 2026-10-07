//go:build !unix

package core

import (
	"io"
	"sync/atomic"
)

// 非 unix 平台不走无缓冲等待（生产只跑 Linux），rawConnOf 一律返回 nil。
const rawCopySupported = false

func (r *relayState) copyRaw(dst io.Writer, rc rawReader, counter *atomic.Int64) (int64, error) {
	return 0, io.ErrUnexpectedEOF
}
