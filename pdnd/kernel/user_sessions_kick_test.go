package kernel

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// alertingOuter 模拟带安全层的外层连接（TLS / REALITY）：Close 先往底层写一条
// 「close_notify」再关底层，写不进去时按 5 秒写截止卡住。
type alertingOuter struct {
	net.Conn
}

func (o *alertingOuter) NetConn() net.Conn { return o.Conn }

func (o *alertingOuter) Close() error {
	_ = o.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = o.Conn.Write([]byte("ALERT"))
	return o.Conn.Close()
}

// TestRevokeClosesUnderlyingFirst：踢人时带安全层的连接先关底层 TCP。原先逐条调
// 外层 Close：对端不读时每条卡满 5 秒写截止、逐条串行；Vision 直通之后那条加密的
// close_notify 还会落进裸流，被对端当成内层数据。
func TestRevokeClosesUnderlyingFirst(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var s userSessions
	user := core.User{ID: 42, UUID: "u"}
	peers := make([]net.Conn, 0, 3)
	for i := 0; i < 3; i++ {
		client, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		server, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		// 对端不读：把服务端的发送缓冲写满，外层 Close 的告警就写不进去。
		_ = server.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
		chunk := make([]byte, 64<<10)
		for {
			if _, err := server.Write(chunk); err != nil {
				break
			}
		}
		_ = server.SetWriteDeadline(time.Time{})
		if s.open(user, s.epoch(), &alertingOuter{Conn: server}) == nil {
			t.Fatal("登记失败")
		}
		peers = append(peers, client)
	}
	start := time.Now()
	s.revoke([]int64{user.ID})
	if took := time.Since(start); took > time.Second {
		t.Fatalf("踢 3 条不读的连接用了 %v", took)
	}
	for i, client := range peers {
		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		data, _ := io.ReadAll(client)
		if n := len(data); n >= 5 && string(data[n-5:]) == "ALERT" {
			t.Fatalf("第 %d 条连接的流里出现了外层告警", i)
		}
	}
}
