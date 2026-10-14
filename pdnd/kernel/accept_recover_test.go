package kernel

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// 单条连接的处理 panic 不能带走 Accept 循环（更不能带走进程）：兜住、关掉
// 这条连接、计数，下一条照常接。
func TestRunAcceptLoopSurvivesHandlerPanic(t *testing.T) {
	conns := make(chan net.Conn, 3)
	var peers []net.Conn
	for i := 0; i < 3; i++ {
		server, client := net.Pipe()
		conns <- server
		peers = append(peers, client)
	}
	close(conns)
	defer func() {
		for _, p := range peers {
			_ = p.Close()
		}
	}()
	accept := func() (net.Conn, error) {
		c, ok := <-conns
		if !ok {
			return nil, net.ErrClosed
		}
		return c, nil
	}
	before := ConnHandlerPanics()
	var handled int
	runAcceptLoopWith(make(chan struct{}), accept, func(c net.Conn) {
		handled++
		if handled == 1 {
			panic("malformed packet")
		}
	}, func(<-chan struct{}, time.Duration) bool { return true })
	if handled != 3 {
		t.Fatalf("panic 之后 Accept 循环只处理了 %d 条连接，期望 3 条", handled)
	}
	if got := ConnHandlerPanics() - before; got != 1 {
		t.Fatalf("兜住的 panic 计数 = %d，期望 1", got)
	}
	// 出事的那条连接被关掉了：对端读到 EOF 而不是一直挂着。
	_ = peers[0].SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peers[0].Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("panic 的连接没有被关闭：err=%v", err)
	}
}

// goGuardedConn：处理 goroutine 里的 panic 只断这一条连接，fn 自己的 defer
// （wg.Done 之类）照常执行。
func TestGoGuardedConnRecoversAndRunsDefers(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	before := ConnHandlerPanics()
	var wg sync.WaitGroup
	wg.Add(1)
	goGuardedConn(server, func() {
		defer wg.Done()
		var m map[string]int
		m["boom"]++ // nil map 写入
	})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("panic 之后 fn 的 defer 没有执行，wg 永远等不到")
	}
	deadline := time.Now().Add(2 * time.Second)
	for ConnHandlerPanics()-before != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ConnHandlerPanics() - before; got != 1 {
		t.Fatalf("兜住的 panic 计数 = %d，期望 1", got)
	}
}
