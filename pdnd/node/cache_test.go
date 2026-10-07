package node

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	nativekernel "github.com/aegispanel/nodeagent/kernel"
	"github.com/aegispanel/nodeagent/panel"
)

// ---------------------------------------------------------------------------
// 夹具：可以「宕机」的签名面板
// ---------------------------------------------------------------------------

// signedWorld 是一个签名面板加一份节点身份。down 之后面板一律断连（模拟宕机），
// 同一份身份可以造多个节点——「重启」就是用同一身份、同一缓存目录再造一个。
type signedWorld struct {
	fake     *fakeSignedPanel
	srv      *httptest.Server
	identity panel.Identity
	cacheDir string
	mu       sync.Mutex
	down     bool
	// reject 非零时 effective-config 回这个状态码（面板明确拒绝）。
	reject int
	// heartbeats 记下收到的心跳正文与原因请求头。
	heartbeats []recordedHeartbeat
}

type recordedHeartbeat struct {
	body   map[string]any
	reason string
}

func newSignedWorld(t *testing.T) *signedWorld {
	t.Helper()
	configPub, configPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keySum := sha256.Sum256(configPub)
	keyID := base64.RawURLEncoding.EncodeToString(keySum[:8])
	uni := &fakeUniProxy{}
	uni.setUsers(`"users-1"`, "user-a", "user-b")
	w := &signedWorld{
		fake:     &fakeSignedPanel{uni: uni, priv: configPriv, keyID: keyID, honorApplied: true},
		cacheDir: t.TempDir(),
	}
	w.fake.publish(releaseGood, 1, 18080)
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		down, reject := w.down, w.reject
		w.mu.Unlock()
		if down {
			// 像宕机的面板：连接直接断掉，没有任何 HTTP 答复。
			if hj, ok := rw.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/v1/nodes/heartbeat" {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			w.mu.Lock()
			w.heartbeats = append(w.heartbeats, recordedHeartbeat{body: body, reason: r.Header.Get(panel.RuntimeReasonHeader)})
			w.mu.Unlock()
			_ = json.NewEncoder(rw).Encode(map[string]any{"node_status": "active"})
			return
		}
		if reject != 0 && r.URL.Path == "/v1/nodes/effective-config" {
			http.Error(rw, "rejected", reject)
			return
		}
		w.fake.ServeHTTP(rw, r)
	}))
	t.Cleanup(w.srv.Close)
	w.identity = panel.Identity{
		Server: w.srv.URL, NodeID: testNodeID, Serial: 1,
		PrivateKey:      base64.StdEncoding.EncodeToString(nodePriv),
		ConfigPublicKey: base64.StdEncoding.EncodeToString(configPub),
		ConfigKeyID:     keyID,
	}
	return w
}

func (w *signedWorld) setDown(down bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.down = down
}

// newNode 造一个挂着落盘缓存的签名节点（模拟一次进程启动）。
func (w *signedWorld) newNode(t *testing.T, kernel core.Core) *Node {
	t.Helper()
	identity := w.identity
	signed, err := panel.NewSignedClient(&identity)
	if err != nil {
		t.Fatal(err)
	}
	client := panel.New(panel.Options{BaseURL: w.srv.URL, NodeID: testNodeID, NodeType: "vless", Token: "token",
		Timeout: 2 * time.Second})
	n := NewWithSignedClient(client, kernel, testLogger(), signed)
	n.SetCacheDir(w.cacheDir)
	return n
}

// ---------------------------------------------------------------------------
// 回归测试
// ---------------------------------------------------------------------------

// 面板宕机时重启节点：用落盘的、重新验过签的配置与用户名单照常起服务，
// 状态如实报 degraded（serving_cached_config）；面板回来后切回 running。
//
// 原先 pdnd 只落盘 identity.json，这种情况下节点彻底停服直到面板恢复。
func TestSignedNodeStartsFromCacheWhenPanelDown(t *testing.T) {
	w := newSignedWorld(t)
	first := w.newNode(t, newUserTableCore())
	first.startup(context.Background())
	if !first.started {
		t.Fatal("面板正常时首次启动没有装上配置")
	}
	for _, name := range []string{"config.json", "users.json"} {
		path := filepath.Join(first.cache.dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("没有落盘 %s：%v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s 权限 %v，期望 0600", name, info.Mode().Perm())
		}
	}

	w.setDown(true)
	kernel := newUserTableCore()
	restarted := w.newNode(t, kernel)
	restarted.startup(context.Background())
	if !restarted.started || kernel.port != 18080 {
		t.Fatalf("面板宕机时重启没有用缓存起服务：started=%v port=%d", restarted.started, kernel.port)
	}
	assertKernelUsers(t, kernel, "缓存起服务之后", "user-a", "user-b")
	if status, reason := restarted.runtimeHealth(); status != runtimeDegraded || reason != reasonServingCached {
		t.Fatalf("缓存起服务时上报 %s/%s，期望 degraded/%s", status, reason, reasonServingCached)
	}

	// 面板回来：已应用版本仍是当前版（204），用户名单 ETag 也对得上（304），不重建入站。
	w.setDown(false)
	before := len(w.fake.uni.userIfNoneMatch)
	restarted.syncOnce(context.Background())
	if status, _ := restarted.runtimeHealth(); status != runtimeRunning {
		t.Fatalf("面板恢复后仍报 %s", status)
	}
	w.fake.mu.Lock()
	unchanged := w.fake.unchangedReplies
	w.fake.mu.Unlock()
	if unchanged == 0 {
		t.Fatal("面板恢复后没有带上缓存里的已应用版本（应当换到 204）")
	}
	if got := w.fake.uni.lastUserIfNoneMatch(); got != `"users-1"` || len(w.fake.uni.userIfNoneMatch) == before {
		t.Fatalf("面板恢复后拉用户带的 If-None-Match = %s，期望缓存里的 \"users-1\"", got)
	}
}

// 缓存被改过（签名对不上）：丢弃缓存，fail-closed，不起服务。
func TestSignedCacheWithBadSignatureIsDiscarded(t *testing.T) {
	w := newSignedWorld(t)
	first := w.newNode(t, newUserTableCore())
	first.startup(context.Background())
	path := filepath.Join(first.cache.dir, "config.json")
	var file cachedConfigFile
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	file.Signed.Payload = json.RawMessage(`{"server_port":18081,"protocol":"vless"}`)
	tampered, _ := json.Marshal(file)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	w.setDown(true)
	kernel := newUserTableCore()
	restarted := w.newNode(t, kernel)
	restarted.startup(context.Background())
	if restarted.started || kernel.port != 0 {
		t.Fatalf("改过的缓存被装上了：started=%v port=%d", restarted.started, kernel.port)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("验签不过的缓存没有被删掉：%v", err)
	}
	if status, reason := restarted.runtimeHealth(); status != runtimeDegraded || reason != reasonNotStarted {
		t.Fatalf("没起来却报 %s/%s", status, reason)
	}
}

// 面板明确拒绝这个节点（身份吊销、节点被删）不是「不可达」：不拿缓存绕过去。
func TestSignedCacheNotUsedWhenPanelRejects(t *testing.T) {
	w := newSignedWorld(t)
	first := w.newNode(t, newUserTableCore())
	first.startup(context.Background())

	w.mu.Lock()
	w.reject = http.StatusUnauthorized
	w.mu.Unlock()
	kernel := newUserTableCore()
	restarted := w.newNode(t, kernel)
	restarted.startup(context.Background())
	if restarted.started {
		t.Fatal("面板明确拒绝后仍用缓存起了服务")
	}
}

// ---------------------------------------------------------------------------
// 兼容通道 + 真 NativeCore：面板宕机重启后入站照常监听、用户能认证
// ---------------------------------------------------------------------------

// socksPanel 是只下发 socks 入站的 UniProxy 假面板，可宕机。
type socksPanel struct {
	mu      sync.Mutex
	port    int
	down    bool
	served  int
	pushes  []http.Header
	reports []map[string][2]int64
}

func (p *socksPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	switch r.URL.Path {
	case "/api/v1/server/UniProxy/config":
		if r.Header.Get("If-None-Match") == `"socks-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		p.served++
		w.Header().Set("ETag", `"socks-1"`)
		_ = json.NewEncoder(w).Encode(map[string]any{"server_port": p.port, "protocol": "socks", "listen_ip": "127.0.0.1"})
	case "/api/v1/server/UniProxy/user":
		if r.Header.Get("If-None-Match") == `"u-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"u-1"`)
		_ = json.NewEncoder(w).Encode(map[string]any{"users": []map[string]any{
			{"id": 1, "uuid": "alice-uuid-0001"}, {"id": 2, "uuid": "bob-uuid-00002"},
		}})
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func newNativeKernel(t *testing.T) *nativekernel.NativeCore {
	t.Helper()
	k := nativekernel.NewNativeCore(nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := k.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// socks5Auth 走一遍 SOCKS5 用户名/口令认证（pdnd 的 socks 入站两者都是用户 UUID），
// 返回认证是否通过。
func socks5Auth(t *testing.T, port int, uuid string) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("入站没有在监听：%v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 2 {
		t.Fatalf("SOCKS5 方法协商失败：%v %v", reply, err)
	}
	req := []byte{1, byte(len(uuid))}
	req = append(req, uuid...)
	req = append(req, byte(len(uuid)))
	req = append(req, uuid...)
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		return false
	}
	return reply[1] == 0
}

func TestCompatNodeServesUsersFromCacheWhenPanelDown(t *testing.T) {
	fake := &socksPanel{port: freeTCPPort(t)}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cacheDir := t.TempDir()
	newNode := func(kernel core.Core) *Node {
		client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: "7", NodeType: "socks", Token: "token",
			Timeout: 2 * time.Second})
		n := New(client, kernel, testLogger())
		n.SetCacheDir(cacheDir)
		return n
	}

	firstKernel := newNativeKernel(t)
	first := newNode(firstKernel)
	first.startup(context.Background())
	if !first.started || !socks5Auth(t, fake.port, "alice-uuid-0001") {
		t.Fatal("面板正常时首次启动没有起来")
	}
	// 「进程退出」：关掉内核，端口让出来。
	_ = firstKernel.Close()

	fake.mu.Lock()
	fake.down = true
	fake.mu.Unlock()
	restarted := newNode(newNativeKernel(t))
	restarted.startup(context.Background())
	if !restarted.started {
		t.Fatal("面板宕机时重启没有用缓存起服务")
	}
	if !socks5Auth(t, fake.port, "alice-uuid-0001") {
		t.Fatal("缓存起服务后已授权用户认证不过")
	}
	if socks5Auth(t, fake.port, "mallory-uuid-999") {
		t.Fatal("缓存起服务后未授权用户也认证通过了")
	}

	// 面板回来：配置 ETag 来自缓存，换到 304，不再拉全量、不重建入站。
	fake.mu.Lock()
	fake.down = false
	servedBefore := fake.served
	fake.mu.Unlock()
	restarted.syncOnce(context.Background())
	fake.mu.Lock()
	servedAfter := fake.served
	fake.mu.Unlock()
	if servedAfter != servedBefore {
		t.Fatalf("面板恢复后又拉了一次全量配置（%d → %d），缓存里的 ETag 没用上", servedBefore, servedAfter)
	}
	if status, _ := restarted.runtimeHealth(); status != runtimeRunning {
		t.Fatalf("面板恢复后仍报 %s", status)
	}
}
