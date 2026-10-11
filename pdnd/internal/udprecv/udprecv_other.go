//go:build !unix

package udprecv

import "errors"

// Supported：非 unix 平台没有这套收包（节点只在 Linux 上部署），调用方退回阻塞 ReadFrom。
const Supported = false

type batchSys struct{}

func (s *batchSys) init([][]byte) {}

func (b *Batch) read(uintptr, int) (int, error) {
	return 0, errors.New("udprecv: unsupported platform")
}

func peek(uintptr) error { return errors.New("udprecv: unsupported platform") }
