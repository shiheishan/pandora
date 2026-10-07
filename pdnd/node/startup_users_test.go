package node

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	nativekernel "github.com/aegispanel/nodeagent/kernel"
	"github.com/aegispanel/nodeagent/panel"
)

// recordingKernel 包一层真 NativeCore，记下名单是随入站一起装的、还是事后补的。
type recordingKernel struct {
	*nativekernel.NativeCore
	mu         sync.Mutex
	withUsers  [][]core.User
	addedLater int
}

func (k *recordingKernel) ApplyInboundWithUsers(cfg *core.InboundConfig, routing *core.Routing, users []core.User) error {
	k.mu.Lock()
	k.withUsers = append(k.withUsers, append([]core.User(nil), users...))
	k.mu.Unlock()
	return k.NativeCore.ApplyInboundWithUsers(cfg, routing, users)
}

func (k *recordingKernel) AddUsers(tag string, users []core.User) error {
	k.mu.Lock()
	k.addedLater += len(users)
	k.mu.Unlock()
	return k.NativeCore.AddUsers(tag, users)
}

func (k *recordingKernel) snapshot() ([][]core.User, int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([][]core.User(nil), k.withUsers...), k.addedLater
}

// 启动顺序：冷启动时名单随入站一起装（内核先装名单、再 accept），不再是
// 「入站先起、名单后补」；紧接着那轮同步换回 304，不重复拉全量。
func TestColdStartInstallsUsersBeforeAccept(t *testing.T) {
	fake := &socksPanel{port: freeTCPPort(t)}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	kernel := &recordingKernel{NativeCore: newNativeKernel(t)}
	client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: "7", NodeType: "socks", Token: "token", Timeout: 2 * time.Second})
	n := New(client, kernel, testLogger())
	n.startup(context.Background())
	if !n.started {
		t.Fatal("冷启动没有装上入站")
	}
	withUsers, later := kernel.snapshot()
	if len(withUsers) != 1 || len(withUsers[0]) != 2 {
		t.Fatalf("名单应随入站一起装（2 人），实际 %v", withUsers)
	}
	if later != 0 {
		t.Fatalf("名单已随入站装好，不应再补 AddUsers（补了 %d 人）", later)
	}
	if !socks5Auth(t, fake.port, "alice-uuid-0001") || socks5Auth(t, fake.port, "mallory-uuid-999") {
		t.Fatal("随入站装的名单没有生效")
	}
	if len(n.known) != 2 {
		t.Fatalf("本地镜像应对齐到装进去的名单，known=%d", len(n.known))
	}
	// 下一轮同步：用户 ETag 就是刚拉的那版，换回 304，名单不动。
	n.syncOnce(context.Background())
	if _, later := kernel.snapshot(); later != 0 {
		t.Fatalf("下一轮同步不该再加人（加了 %d）", later)
	}
}

// 面板不可达、走落盘缓存起服务：名单同样随入站一起装（用落盘的那份），不等面板。
func TestOfflineStartInstallsCachedUsersWithInbound(t *testing.T) {
	fake := &socksPanel{port: freeTCPPort(t)}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cacheDir := t.TempDir()
	newNode := func(kernel core.Core) *Node {
		client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: "7", NodeType: "socks", Token: "token", Timeout: 2 * time.Second})
		n := New(client, kernel, testLogger())
		n.SetCacheDir(cacheDir)
		return n
	}
	firstKernel := newNativeKernel(t)
	first := newNode(firstKernel)
	first.startup(context.Background())
	if !first.started {
		t.Fatal("首次启动没有起来")
	}
	_ = firstKernel.Close()
	fake.mu.Lock()
	fake.down = true
	fake.mu.Unlock()
	kernel := &recordingKernel{NativeCore: newNativeKernel(t)}
	restarted := newNode(kernel)
	restarted.startup(context.Background())
	if !restarted.started {
		t.Fatal("面板宕机时没有用缓存起服务")
	}
	withUsers, _ := kernel.snapshot()
	if len(withUsers) != 1 || len(withUsers[0]) != 2 {
		t.Fatalf("缓存起服务时名单应随入站一起装，实际 %v", withUsers)
	}
	if !socks5Auth(t, fake.port, "alice-uuid-0001") {
		t.Fatal("缓存名单没有生效")
	}
}
