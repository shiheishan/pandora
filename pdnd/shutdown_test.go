package main

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

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
	return map[string][]core.UserTraffic{"n1": {{ID: 9, Upload: 300, Download: 700}}}, nil
}

// ackLink 是假会话：Shutdown 时先给一个 Welcome，收到流量就记一笔并立刻回 TrafficAck。
type ackLink struct {
	kernel *drainingCore
	events chan panel.Event

	mu      sync.Mutex
	reports []*nodev1.TrafficReport
}

func (l *ackLink) Events() <-chan panel.Event           { return l.events }
func (l *ackLink) SendHeartbeat(*nodev1.Heartbeat) bool { return true }
func (l *ackLink) SendOnline(*nodev1.OnlineIPs) bool    { return true }
func (l *ackLink) SendConfigAck(*nodev1.ConfigAck) bool { return true }
func (l *ackLink) SendUsersAck(*nodev1.UsersAck) bool   { return true }
func (l *ackLink) SendTraffic(r *nodev1.TrafficReport) bool {
	l.kernel.record("push")
	l.mu.Lock()
	l.reports = append(l.reports, r)
	l.mu.Unlock()
	l.events <- panel.Event{Kind: panel.EventTrafficAck, ReportID: r.GetReportId()}
	return true
}

// 退出顺序：先关内核（在途 TCP 连接此时把流量入账），再把这最后一轮经会话报给面板、等它确认。
// 原先是先报、再由 defer 关内核，在途连接的流量每次重启都丢。
func TestShutdownClosesKernelBeforeFinalReport(t *testing.T) {
	kernel := &drainingCore{}
	link := &ackLink{kernel: kernel, events: make(chan panel.Event, 8)}
	srv := node.New(node.Options{ServerID: "0190a000-0000-7000-8000-000000000001", Kernel: kernel, Logger: slog.New(slog.DiscardHandler)})
	// 主循环已停：用取消了的 ctx 跑一下 Run，只为把会话挂上去。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv.Run(ctx, link)
	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	done := make(chan struct{})
	close(done)
	shutdown(slog.New(slog.DiscardHandler), kernel, srv, done)

	if len(kernel.events) != 2 || kernel.events[0] != "close-and-drain" || kernel.events[1] != "push" {
		t.Fatalf("关停顺序 = %v，期望先关内核交流量、再上报", kernel.events)
	}
	if len(link.reports) != 1 || len(link.reports[0].GetEntries()) != 1 {
		t.Fatalf("最后一轮流量没有报上去：%v", link.reports)
	}
	e := link.reports[0].GetEntries()[0]
	if e.GetNodeId() != "n1" || e.GetUid() != 9 || e.GetUpload() != 300 || e.GetDownload() != 700 {
		t.Fatalf("最后一轮流量 = %v", e)
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

// 状态目录（落盘缓存、身份目录）必须在 systemd 单元的可写路径里：ProtectSystem=strict 下
// 只有 ReadWritePaths 可写，不在里面的话缓存写失败、证书续期换不上（到期后节点连不上面板）。
func TestReleaseUnitAllowsWritingStateDirectory(t *testing.T) {
	if !strings.HasPrefix(panel.DefaultIdentityDir, node.DefaultCacheDir+"/") {
		t.Fatalf("身份目录 %s 不在状态目录 %s 下", panel.DefaultIdentityDir, node.DefaultCacheDir)
	}
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
