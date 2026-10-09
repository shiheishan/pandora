//go:build unix

package udprecv

import (
	"bytes"
	"errors"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func listen(t *testing.T, network, loopback string) (*net.UDPConn, *Receiver) {
	t.Helper()
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: net.ParseIP(loopback)})
	if err != nil {
		t.Skipf("本机没有 %s 回环：%v", network, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	rc, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	r := NewReceiver(rc)
	if r == nil {
		t.Fatal("unix 上应有收包器")
	}
	return conn, r
}

func newTestBatch(n int) *Batch {
	bufs := make([][]byte, n)
	for i := range bufs {
		bufs[i] = make([]byte, 2048)
	}
	return NewBatch(bufs)
}

// Ready 等包期间不占缓冲，落空时也不借组：第一次回调只窥视（不借），被 netpoller
// 叫醒后才借组收；一次醒来两次系统调用（窥视落空 + 收到）、借一次。连等两轮（第二轮
// 紧跟在收空之后，正是会话收完一串再去等的形状）。包已在队列里时窥视到就借组收。
// 收到的负载与来源（IPv4 与 IPv6，IPv6 来源不被当成 IPv4 映射地址）正确。
func TestReadyHoldsNoBatchWhileWaiting(t *testing.T) {
	for _, network := range []struct{ name, loopback string }{{"udp4", "127.0.0.1"}, {"udp6", "::1"}} {
		t.Run(network.name, func(t *testing.T) {
			conn, r := listen(t, network.name, network.loopback)
			sender, err := net.ListenUDP(network.name, &net.UDPAddr{IP: net.ParseIP(network.loopback)})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			var borrowed, returned atomic.Int64
			batch := newTestBatch(2)
			borrow := func() *Batch { borrowed.Add(1); return batch }
			giveBack := func(*Batch) { returned.Add(1) }
			type result struct {
				b   *Batch
				n   int
				err error
			}
			done := make(chan result, 1)
			payload := bytes.Repeat([]byte{7}, 1200)
			want := sender.LocalAddr().(*net.UDPAddr).AddrPort()
			want = netip.AddrPortFrom(want.Addr().Unmap(), want.Port())
			check := func(res result) {
				t.Helper()
				if res.err != nil || res.n != 1 || res.b != batch {
					t.Fatalf("Ready n=%d err=%v", res.n, res.err)
				}
				if !bytes.Equal(batch.Bufs[0][:batch.N[0]], payload) {
					t.Fatalf("负载不一致：%d 字节", batch.N[0])
				}
				if batch.From[0] != want {
					t.Fatalf("来源 %v，期望 %v", batch.From[0], want)
				}
			}
			for round := range 2 {
				borrowBefore, returnBefore, recvsBefore := borrowed.Load(), returned.Load(), r.Recvs()
				go func() {
					b, n, err := r.Ready(2, borrow, giveBack)
					done <- result{b, n, err}
				}()
				// 等它窥视落空、挂到 netpoller 上。
				deadline := time.Now().Add(2 * time.Second)
				for r.Recvs() == recvsBefore {
					if time.Now().After(deadline) {
						t.Fatal("Ready 没有开始等")
					}
					runtime.Gosched()
				}
				time.Sleep(20 * time.Millisecond)
				if got := borrowed.Load() - borrowBefore; got != 0 {
					t.Fatalf("第 %d 轮：等包期间借了 %d 次组", round, got)
				}
				if _, err := sender.WriteTo(payload, conn.LocalAddr()); err != nil {
					t.Fatal(err)
				}
				check(<-done)
				if got := borrowed.Load() - borrowBefore; got != 1 {
					t.Fatalf("第 %d 轮：一次醒来借了 %d 次组，期望 1 次", round, got)
				}
				if got := returned.Load() - returnBefore; got != 0 {
					t.Fatalf("第 %d 轮：收到前还了 %d 次组（落空时借过）", round, got)
				}
				if got := r.Recvs() - recvsBefore; got > 3 {
					t.Fatalf("第 %d 轮：一次醒来收了 %d 次，期望 2 次（容一次虚假唤醒）", round, got)
				}
				returned.Add(1)
			}
			// 包已在队列里：窥视到、借组收，不等。
			if _, err := sender.WriteTo(payload, conn.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
			borrowBefore, recvsBefore := borrowed.Load(), r.Recvs()
			b, n, err := r.Ready(2, borrow, giveBack)
			check(result{b, n, err})
			if borrowed.Load()-borrowBefore != 1 || r.Recvs()-recvsBefore != 2 {
				t.Fatalf("队列里有包：借 %d 次、收 %d 次，期望 1、2", borrowed.Load()-borrowBefore, r.Recvs()-recvsBefore)
			}
		})
	}
}

// 非阻塞收：空了返回 ErrWouldBlock；积压的包一次收一批（Linux recvmmsg），别的
// unix 一次一包。读截止打断 Ready 的等待，组已还回。
func TestRecvBatchAndDeadline(t *testing.T) {
	conn, r := listen(t, "udp4", "127.0.0.1")
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	batch := newTestBatch(4)
	if _, err := r.Recv(batch, 4, false); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("空 socket 非阻塞收：%v", err)
	}
	for i := range 3 {
		if _, err := sender.WriteTo([]byte{byte(i + 1)}, conn.LocalAddr()); err != nil {
			t.Fatal(err)
		}
	}
	// 等三包都进了接收队列（Linux 上才能一次收齐）。
	time.Sleep(20 * time.Millisecond)
	got := 0
	want := 1
	if runtime.GOOS == "linux" {
		want = 3
	}
	n, err := r.Recv(batch, 4, true)
	if err != nil || n != want {
		t.Fatalf("阻塞收 n=%d err=%v，期望 %d", n, err, want)
	}
	for i := range n {
		if batch.N[i] != 1 || batch.Bufs[i][0] != byte(got+1) {
			t.Fatalf("第 %d 包内容不对", got)
		}
		got++
	}
	for got < 3 {
		n, err := r.Recv(batch, 4, false)
		if err != nil {
			t.Fatalf("收剩下的：%v", err)
		}
		got += n
	}

	var borrowed, returned atomic.Int64
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, _, err = r.Ready(4, func() *Batch { borrowed.Add(1); return batch }, func(*Batch) { returned.Add(1) })
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("读截止到点：%v", err)
	}
	if borrowed.Load() != returned.Load() {
		t.Fatalf("读截止返回后还借着：借 %d 还 %d", borrowed.Load(), returned.Load())
	}
}
