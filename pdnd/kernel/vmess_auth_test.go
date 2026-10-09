package kernel

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
)

// captureConn 把客户端写出的字节全收下，用来拿一份真实的 VMess 请求头。
type captureConn struct {
	net.Conn
	buf bytes.Buffer
}

func (c *captureConn) Write(p []byte) (int, error) { return c.buf.Write(p) }
func (c *captureConn) Read([]byte) (int, error)    { select {} }

func vmessRequestBytes(t testing.TB, id string) []byte {
	t.Helper()
	client, err := vmessref.NewClient(id, "aes-128-gcm", 0)
	if err != nil {
		t.Fatal(err)
	}
	capture := &captureConn{}
	conn := client.DialEarlyConn(capture, M.ParseSocksaddrHostPort("127.0.0.1", 443))
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	return capture.buf.Bytes()
}

func newVMessAuthAdapter(t testing.TB, n int) (*vmessAdapter, []core.User) {
	t.Helper()
	a := &vmessAdapter{users: make(map[string]vmessUser), active: make(map[net.Conn]struct{})}
	users := make([]core.User, n)
	for i := range users {
		users[i] = core.User{ID: int64(i + 1), UUID: uuid.NewString()}
	}
	if err := a.AddUsers(users); err != nil {
		t.Fatal(err)
	}
	return a, users
}

// 认证快照随用户增删更新：删掉的用户立刻认证不过，新加的立刻认证得过。
func TestVMessAuthSnapshotFollowsUserChanges(t *testing.T) {
	a, users := newVMessAuthAdapter(t, 50)
	req := vmessRequestBytes(t, users[49].UUID)
	user, _, _, _, err := a.readRequest(bufio.NewReader(bytes.NewReader(req)))
	if err != nil || user.ID != 50 {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	if err := a.DelUsers([]string{users[49].UUID}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := a.readRequest(bufio.NewReader(bytes.NewReader(req))); err == nil {
		t.Fatal("删掉的用户仍然认证通过")
	}
	extra := core.User{ID: 999, UUID: uuid.NewString()}
	if err := a.UpsertUsers([]core.User{extra}); err != nil {
		t.Fatal(err)
	}
	req = vmessRequestBytes(t, extra.UUID)
	if user, _, _, _, err := a.readRequest(bufio.NewReader(bytes.NewReader(req))); err != nil || user.ID != 999 {
		t.Fatalf("新加的用户认证不过：user=%+v err=%v", user, err)
	}
}

// 5000 用户、命中最后一个（最坏情况）的单次握手认证 CPU。
func BenchmarkVMessAuth5000Users(b *testing.B) {
	a, users := newVMessAuthAdapter(b, 5000)
	// map 遍历无序，挑快照里排最后的那个用户，量最坏情况。
	snapshot := *a.authCandidates.Load()
	last := snapshot[len(snapshot)-1].user.UUID
	_ = users
	req := vmessRequestBytes(b, last)
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, _, _, _, err := a.readRequest(bufio.NewReader(bytes.NewReader(req))); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(time.Since(start).Microseconds())/float64(b.N), "µs/handshake")
}
