//go:build unix

package core

import (
	"io"
	"sync/atomic"
	"syscall"
)

const rawCopySupported = true

// copyRaw 从裸 TCP 读：在 RawConn.Read 的回调里才从池里借缓冲做一次非阻塞读，
// 读不到（EAGAIN）就把缓冲还回去、交给 netpoller 等可读。空闲连接因此不持有
// 任何拷贝缓冲；读截止时间与 Close 照常生效（等待在 netpoller 里）。
func (r *relayState) copyRaw(dst io.Writer, rc rawReader, counter *atomic.Int64) (int64, error) {
	var written int64
	for {
		var bp *[]byte
		var n int
		var readErr error
		err := rc.Read(func(fd uintptr) bool {
			if bp == nil {
				bp = getBigBuf()
			}
			for {
				n, readErr = syscall.Read(int(fd), *bp)
				if readErr != syscall.EINTR {
					break
				}
			}
			if readErr == syscall.EAGAIN {
				putBigBuf(bp)
				bp = nil
				return false
			}
			return true
		})
		if err != nil {
			if bp != nil {
				putBigBuf(bp)
			}
			return written, err
		}
		if readErr != nil {
			putBigBuf(bp)
			return written, readErr
		}
		if n <= 0 {
			putBigBuf(bp)
			return written, nil
		}
		nw, writeErr := r.emit(dst, (*bp)[:n], counter)
		putBigBuf(bp)
		written += int64(nw)
		if writeErr != nil {
			return written, writeErr
		}
	}
}
