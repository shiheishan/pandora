// [INPUT]: 依赖 signed_e2e_pg18_test.go 的 doSignedJSON / mustJSON 与已接入节点的签名凭据，依赖 pgxpool 直查 node_metrics 与 nodes
// [OUTPUT]: 对外提供 checkHeartbeatMetricsRange（由 TestSignedNodeHTTPPG18 调用，随 run-pg18-gates.sh 的 effective 域跑）
// [POS]: api/node 的 PG18 断言：签名心跳的 metrics 越界回中文 400、整条心跳不落库（不写探针点、不刷新 last_heartbeat_at），合法值照常写一行

package node

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

// checkHeartbeatMetricsRange 在一个已接入的节点上核对心跳 metrics 的范围校验。
// 过去 cpu_bp 越界撞 node_metrics 的 CHECK、int 字段超出 int4 列宽，都会把整条
// 心跳事务回滚成 500。
func checkHeartbeatMetricsRange(t *testing.T, ctx context.Context, admin *pgxpool.Pool, privateKey ed25519.PrivateKey,
	tenantID, nodeID, heartbeatURL string) {
	t.Helper()
	state := func() (points int, last time.Time) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_metrics WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT last_heartbeat_at FROM nodes WHERE tenant_id=$1 AND id=$2::uuid)`, tenantID, nodeID).
			Scan(&points, &last); err != nil {
			t.Fatal(err)
		}
		return
	}
	beat := func(metrics map[string]any, want int) string {
		t.Helper()
		var body struct {
			Error struct{ Message string } `json:"error"`
		}
		doSignedJSON(t, privateKey, nodeID, http.MethodPost, heartbeatURL,
			mustJSON(t, map[string]any{"agent_version": "e2e-test", "metrics": metrics}), want, &body)
		return body.Error.Message
	}
	basePoints, baseLast := state()
	for name, metrics := range map[string]map[string]any{
		"cpu_bp above 10000":      {"cpu_bp": 10001},
		"cpu_bp negative":         {"cpu_bp": -1},
		"mem_used_mb beyond int4": {"cpu_bp": 100, "mem_used_mb": int64(1) << 31},
		"tcp_conns negative":      {"tcp_conns": -5},
		"net_rx_bytes negative":   {"net_rx_bytes": -1},
	} {
		msg := beat(metrics, http.StatusBadRequest)
		if !hasHan(msg) {
			t.Fatalf("%s: message %q is not Chinese", name, msg)
		}
		if points, last := state(); points != basePoints || !last.Equal(baseLast) {
			t.Fatalf("%s: rejected heartbeat was persisted (points %d→%d, last %s→%s)", name, basePoints, points, baseLast, last)
		}
	}
	beat(map[string]any{"cpu_bp": 10000, "mem_used_mb": 1<<31 - 1, "net_rx_bytes": int64(1) << 40, "uptime_sec": 3600}, http.StatusOK)
	if points, last := state(); points != basePoints+1 || !last.After(baseLast) {
		t.Fatalf("valid boundary heartbeat: points %d→%d, last %s→%s", basePoints, points, baseLast, last)
	}
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
