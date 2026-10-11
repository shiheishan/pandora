package subscription

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 节点目录纪元（00155）在「掉线后恢复心跳」时推进：心跳间隔超过新鲜窗口的那一次写。窗口必须与
// HeartbeatFreshWindow 相同，否则订阅的节点集合缓存在节点恢复后拿不到信号（窗口比它大），或者
// 例行心跳也推进纪元（窗口比它小）。改窗口要连同最后一个建这条触发器的迁移一起改。
func TestNodeCatalogEpochRecoveryWindowMatchesHeartbeatFreshWindow(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "migrations")
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations not found: %v", err)
	}
	sort.Strings(files)
	var last, lastName string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(raw), "-- +goose Down")
		if strings.Contains(up, "CREATE CONSTRAINT TRIGGER zz_node_catalog_epoch_nodes_update") {
			last, lastName = up, filepath.Base(f)
		}
	}
	if last == "" {
		t.Fatal("no migration creates zz_node_catalog_epoch_nodes_update")
	}
	m := regexp.MustCompile(`OLD\.last_heartbeat_at < NEW\.last_heartbeat_at - interval '(\d+) minutes'`).FindStringSubmatch(last)
	if m == nil {
		t.Fatalf("%s: heartbeat recovery clause not found", lastName)
	}
	minutes, _ := strconv.Atoi(m[1])
	if got := time.Duration(minutes) * time.Minute; got != HeartbeatFreshWindow {
		t.Fatalf("%s: recovery window %s, HeartbeatFreshWindow %s", lastName, got, HeartbeatFreshWindow)
	}
}
