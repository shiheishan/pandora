package core

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// tcpPair 返回一对相连的 TCP 连接。
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b := <-accepted
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

// relayFixture：client ⇄ [clientSide | Relay | upstreamSide] ⇄ upstream。
func relayFixture(t *testing.T, opt RelayOptions) (client, upstream net.Conn, done chan [2]int64) {
	client, clientSide := tcpPair(t)
	upstreamSide, upstream := tcpPair(t)
	done = make(chan [2]int64, 1)
	go func() {
		up, down := Relay(clientSide, upstreamSide, opt)
		done <- [2]int64{up, down}
	}()
	return client, upstream, done
}

func TestRelayCountsAndPropagatesHalfClose(t *testing.T) {
	var up, down atomic.Int64
	client, upstream, done := relayFixture(t, RelayOptions{Up: &up, Down: &down})
	payload := make([]byte, 300<<10) // 跨越小缓冲到大缓冲的切换
	go func() { _, _ = client.Write(payload); _ = client.(*net.TCPConn).CloseWrite() }()
	got, err := io.ReadAll(upstream) // 客户端半关闭必须传到上游
	if err != nil || len(got) != len(payload) {
		t.Fatalf("上游收到 %d 字节 err=%v", len(got), err)
	}
	if up.Load() != int64(len(payload)) {
		t.Fatalf("上行计数=%d", up.Load())
	}
	if _, err := upstream.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	_ = upstream.(*net.TCPConn).CloseWrite()
	back, err := io.ReadAll(client) // 上游半关闭必须传回客户端
	if err != nil || string(back) != "tail" {
		t.Fatalf("客户端收到 %q err=%v", back, err)
	}
	select {
	case n := <-done:
		if n[0] != int64(len(payload)) || n[1] != 4 || down.Load() != 4 {
			t.Fatalf("返回 %v 下行计数 %d", n, down.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("两个方向都结束后 Relay 没有返回")
	}
}

// 客户端半关闭、上游不理会：单向收尾计时到点后收尾。
func TestRelayHalfCloseTimeout(t *testing.T) {
	SetRelayTimeouts(0, 150*time.Millisecond)
	defer SetRelayTimeouts(DefaultRelayIdleTimeout, DefaultRelayHalfCloseTimeout)
	client, _, done := relayFixture(t, RelayOptions{})
	_ = client.(*net.TCPConn).CloseWrite()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("上游不关时，单向收尾计时没有生效")
	}
}

// 两个方向都开着但都不说话：空闲回收。有活动时不回收。
func TestRelayIdleTimeout(t *testing.T) {
	SetRelayTimeouts(300*time.Millisecond, time.Second)
	defer SetRelayTimeouts(DefaultRelayIdleTimeout, DefaultRelayHalfCloseTimeout)
	client, upstream, done := relayFixture(t, RelayOptions{})
	buf := make([]byte, 1)
	for i := 0; i < 4; i++ { // 0.6 秒里一直有心跳，不能被回收
		time.Sleep(150 * time.Millisecond)
		_, _ = client.Write([]byte{1})
		_, _ = io.ReadFull(upstream, buf)
	}
	select {
	case <-done:
		t.Fatal("有活动的连接被空闲回收了")
	default:
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("空闲连接没有被回收")
	}
}

// 出错（客户端被强关）立刻关两端，不等计时。
func TestRelayErrorTearsDownBoth(t *testing.T) {
	client, upstream, done := relayFixture(t, RelayOptions{})
	_ = client.(*net.TCPConn).SetLinger(0)
	_ = client.Close() // RST
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("客户端重置后 Relay 没有收尾")
	}
	_ = upstream.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := upstream.Read(make([]byte, 1)); err == nil {
		t.Fatal("上游应被关掉")
	}
}
