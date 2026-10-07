//go:build interop

package kernel

// AnyTLS 的连接生命周期：子流关闭即释放、删用户即断线。客户端用 sing-anytls，
// 它自带数据竞争（见 anytls_client_interop_test.go 文件头），所以放在非 race 的
// interop 门里；测试名带 Client，CI 的 -run 'Interop|External|Client' 会跑到。

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/util"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
)

func startAnyTLSLifecycle(t *testing.T) (*NativeCore, string, *anytls.Client, *lifecycleEcho, core.User) {
	t.Helper()
	echo := startLifecycleEcho(t, false)
	user := core.User{ID: 6100, UUID: "anytls-lifecycle-secret"}
	c, port, tag := startLifecycleCore(t, lifecycleProto{name: "anytls", raw: map[string]any{}}, []core.User{user})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dialOut := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	}
	client, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: user.UUID, DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return c, tag, client, echo, user
}

func TestAnyTLSClientCloseReleasesSessions(t *testing.T) {
	c, tag, client, echo, _ := startAnyTLSLifecycle(t)
	const n = 20
	streams := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		stream, err := client.CreateProxy(context.Background(), M.SocksaddrFromNet(echo.addr()))
		if err != nil {
			t.Fatal(err)
		}
		if err := echoOnce(stream, fmt.Sprintf("anytls-%d", i)); err != nil {
			t.Fatal(err)
		}
		streams = append(streams, stream)
	}
	if got := c.liveSessionsForTest(tag); got != n {
		t.Fatalf("在途会话=%d，期望 %d", got, n)
	}
	for _, s := range streams {
		_ = s.Close()
	}
	ok, why := waitFor(2*time.Second, func() (bool, string) {
		live, upstream := c.liveSessionsForTest(tag), echo.active.Load()
		return live == 0 && upstream == 0, fmt.Sprintf("会话=%d 上游连接=%d", live, upstream)
	})
	if !ok {
		t.Fatalf("子流全部关闭 2 秒后仍未释放：%s", why)
	}
}

func TestAnyTLSClientKickedOnUserRemoval(t *testing.T) {
	c, tag, client, echo, user := startAnyTLSLifecycle(t)
	stream, err := client.CreateProxy(context.Background(), M.SocksaddrFromNet(echo.addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := echoOnce(stream, "before-kick"); err != nil {
		t.Fatal(err)
	}
	if err := c.DelUsers(tag, []string{user.UUID}); err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	if _, err := stream.Read(buf); err == nil {
		t.Fatal("删用户后子流应被断开")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("删用户 1 秒后子流仍未断开")
	}
	if live := c.liveSessionsForTest(tag); live != 0 {
		t.Fatalf("删用户后在途会话=%d", live)
	}
}
