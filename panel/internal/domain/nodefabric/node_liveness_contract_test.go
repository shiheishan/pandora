package nodefabric_test

// 外部测试包：nodefabric 不能 import subscription（反向依赖），在这里核对两边的窗口常量。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 在线巡检、迁移 00110 的「离线 → 在线」判定、列表的 stale 与状态筛选必须是同一个窗口，
// 否则界面会在「离线」与「在线」之间漏一次刷新或多一次闪烁。
func TestLivenessWindowsMatchListAndDelivery(t *testing.T) {
	if nodefabric.NodeDeliveryFreshWindow != subscription.HeartbeatFreshWindow {
		t.Fatalf("liveness patrol delivery window %s, subscription.HeartbeatFreshWindow %s",
			nodefabric.NodeDeliveryFreshWindow, subscription.HeartbeatFreshWindow)
	}
	if nodefabric.NodeStaleAfter != 90*time.Second {
		t.Fatalf("NodeStaleAfter=%s; the SQL literals below say 90 seconds", nodefabric.NodeStaleAfter)
	}
	const literal = "interval '90 seconds'"
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00110_nodes_notify_skip_heartbeat.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if !strings.Contains(up, "NEW.last_heartbeat_at - OLD.last_heartbeat_at >= "+literal) {
		t.Error("migration 00110 must notify the offline → online flip at the stale window")
	}
	pkg := sourcetest.Load(t, ".")
	if list := pkg.Decl("Service.queryAdminNodes"); !strings.Contains(list, "n.last_heartbeat_at < now() - "+literal) {
		t.Error("admin node list stale column must use the stale window")
	}
	if filter := pkg.Decl("adminNodeFilterSQL"); strings.Count(filter, literal) != 2 {
		t.Error("admin node list online / offline filters must use the stale window")
	}
}
