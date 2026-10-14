//go:build unix && !race

package core

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
)

// TestCopyRawAllocsPerRun 守卫：热循环不应每块多次分配（改前约 4×块数）。
// -race 下 MemStats 噪声大，见 relay_raw_unix_alloc_test.go 的 build tag。
func TestCopyRawAllocsPerRun(t *testing.T) {
	const (
		blockSize = 16 << 10
		blocks    = 2000
	)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	payload := make([]byte, blockSize)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < blocks; i++ {
			if _, err := conn.Write(payload); err != nil {
				return
			}
		}
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	tcpConn, ok := client.(*net.TCPConn)
	if !ok {
		t.Fatal("期望 TCPConn")
	}
	rc, err := tcpConn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}

	r := newRelayState(nil, nil, RelayOptions{})
	var counter atomic.Int64

	pass := 0
	allocs := testing.AllocsPerRun(1, func() {
		pass++
		if pass == 1 {
			return
		}
		n, err := r.copyRaw(io.Discard, rc, &counter)
		if err != nil && err != io.EOF {
			t.Errorf("copyRaw: %v", err)
		}
		want := int64(blockSize * blocks)
		if n != want {
			t.Errorf("copyRaw 字节数 = %d，期望 %d", n, want)
		}
	})

	t.Logf("copyRaw 搬 %d 块共分配 %.0f 次", blocks, allocs)
	const maxAllocs = blocks / 10
	if allocs > float64(maxAllocs) {
		t.Fatalf("copyRaw 分配次数 = %.0f，阈值 < %d（改前约 %d）", allocs, maxAllocs, 4*blocks)
	}
}
