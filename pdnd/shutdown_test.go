package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/node"
	"github.com/aegispanel/nodeagent/panel"
)

// drainingCore 记录关停顺序：关停时交出「在途连接」的最后一轮流量。
type drainingCore struct {
	mu     sync.Mutex
	events []string
}

func (c *drainingCore) record(e string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *drainingCore) Type() string                                  { return "draining-test" }
func (c *drainingCore) Start(context.Context) error                   { return nil }
func (c *drainingCore) Close() error                                  { c.record("close"); return nil }
func (c *drainingCore) AddInbound(*core.InboundConfig) error          { return nil }
func (c *drainingCore) DelInbound(string) error                       { return nil }
func (c *drainingCore) AddUsers(string, []core.User) error            { return nil }
func (c *drainingCore) UpsertUsers(string, []core.User) error         { return nil }
func (c *drainingCore) DelUsers(string, []string) error               { return nil }
func (c *drainingCore) SetRouting(string, *core.Routing) error        { return nil }
func (c *drainingCore) OnlineIPs(string) map[int64][]string           { return nil }
func (c *drainingCore) GetTraffic(string) ([]core.UserTraffic, error) { return nil, nil }

func (c *drainingCore) CloseAndDrainTraffic() (map[string][]core.UserTraffic, error) {
	c.record("close-and-drain")
	return map[string][]core.UserTraffic{"vless-n1": {{ID: 9, Upload: 300, Download: 700}}}, nil
}

// 退出顺序：先关内核（在途 TCP 连接此时把流量入账），再把这最后一轮报给面板。
// 原先是先报、再由 defer 关内核，在途连接的流量每次重启都丢。
func TestShutdownClosesKernelBeforeFinalReport(t *testing.T) {
	kernel := &drainingCore{}
	var pushed map[string][2]int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/server/UniProxy/push" {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &pushed)
			kernel.record("push")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	n := node.New(client, kernel, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	close(done) // 主循环已停
	shutdown(slog.New(slog.DiscardHandler), kernel, []runningNode{{n: n, done: done}})

	if len(kernel.events) != 2 || kernel.events[0] != "close-and-drain" || kernel.events[1] != "push" {
		t.Fatalf("关停顺序 = %v，期望先关内核交流量、再上报", kernel.events)
	}
	if pushed["9"] != [2]int64{300, 700} {
		t.Fatalf("最后一轮流量没有报上去：%v", pushed)
	}
}

func TestConfigCacheDirDefaultsToStateDirectory(t *testing.T) {
	var cfg config
	if cfg.stateDir() != node.DefaultCacheDir || node.DefaultCacheDir != "/var/lib/pandora-native" {
		t.Fatalf("缺省状态目录 = %q", cfg.stateDir())
	}
	cfg.CacheDir = "/srv/pandora"
	if cfg.stateDir() != "/srv/pandora" {
		t.Fatal("cache_dir 没有生效")
	}
}

// 状态目录（落盘缓存、迁出 /etc 的身份文件）必须在 systemd 单元的可写路径里：
// ProtectSystem=strict 下只有 ReadWritePaths 可写，不在里面的话换钥持久化与缓存
// 全部写失败（身份文件留在 /etc 时正是这样）。
func TestReleaseUnitAllowsWritingStateDirectory(t *testing.T) {
	raw, err := os.ReadFile("release/pandora-native.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == "ReadWritePaths" && slices.Contains(strings.Fields(value), node.DefaultCacheDir) {
			return
		}
	}
	t.Fatalf("单元的 ReadWritePaths 不含状态目录 %s", node.DefaultCacheDir)
}
