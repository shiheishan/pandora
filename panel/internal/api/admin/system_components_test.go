// [INPUT]: 依赖 system_components.go 的 postgresComponent
// [OUTPUT]: 对外提供 TestPostgresComponentStates
// [POS]: api/admin 系统状态 postgres 组件的状态判定单测：down / warn（统计读失败不写 metrics 键）/ ok（三个键齐全），对齐契约 R52

package admin

import (
	"errors"
	"testing"
	"time"
)

func TestPostgresComponentStates(t *testing.T) {
	stats := map[string]any{"size_bytes": int64(1 << 20), "connections": 3, "max_connections": 100}

	if pg := postgresComponent(errors.New("dial"), 0, stats); pg.State != "down" || pg.Message == "" || len(pg.Metrics) != 0 {
		t.Fatalf("unreachable: %+v", pg)
	}
	// 统计读失败时 systemStatus 传进来的是只带 error 的 map
	failed := map[string]any{"error": "读取数据库状态失败"}
	if pg := postgresComponent(nil, time.Millisecond, failed); pg.State != "warn" || pg.Message == "" ||
		len(pg.Metrics) != 0 || pg.LatencyMS == nil {
		t.Fatalf("stats unavailable: %+v", pg)
	}
	pg := postgresComponent(nil, time.Millisecond, stats)
	if pg.State != "ok" || pg.Message != "" || len(pg.Metrics) != 3 {
		t.Fatalf("healthy: %+v", pg)
	}
	for _, k := range postgresDatabaseKeys {
		if pg.Metrics[k] == nil {
			t.Fatalf("healthy metrics missing %s: %+v", k, pg.Metrics)
		}
	}
}
