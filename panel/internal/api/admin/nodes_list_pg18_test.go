package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// nodeListFiltersPG18 是 TestNodeListPagingPG18 的后半段（同一个库、同一组节点，过滤名单不用再加一项）：
// 服务端搜索、状态筛选、按 id 单取，以及列表不带编辑字段。nodes 是 step3Nodes 建的三个节点
// （step3-node-1..3，服务器 step3-server，都从未心跳）。
func nodeListFiltersPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, r http.Handler, nodes []string) {
	t.Helper()
	type row = map[string]json.RawMessage
	get := func(query string) ([]row, int64) {
		t.Helper()
		w := step3Do(t, ctx, r, http.MethodGet, "/v1/nodes"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("node list %s: status=%d body=%s", query, w.Code, w.Body.String())
		}
		var body struct {
			Nodes []row `json:"nodes"`
			Total int64 `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Nodes, body.Total
	}
	expect := func(query string, n int, total int64) []row {
		t.Helper()
		rows, got := get(query)
		if len(rows) != n || got != total {
			t.Fatalf("node list %q: nodes=%d total=%d, want %d/%d", query, len(rows), got, n, total)
		}
		return rows
	}

	// 搜索：名称子串、服务器名（不分大小写）、编号；与分页、翻过最后一页的计数同一口径
	if rows := expect("?q=node-2", 1, 1); string(rows[0]["id"]) != `"`+nodes[1]+`"` {
		t.Fatalf("search by name returned %s", rows[0]["id"])
	}
	expect("?q=STEP3-SERVER", 3, 3)
	expect("?q=step3&limit=2&offset=2", 1, 3)
	expect("?q=no-such-node", 0, 0)
	expect("?q=step3&limit=2&offset=10", 0, 3)

	// 状态：从未心跳的在役节点是「离线」；心跳一次变「在线」；停用的进「已停用」
	expect("?state=offline", 3, 3)
	expect("?state=online", 0, 0)
	step3Seed(t, ctx, admin, `UPDATE nodes SET last_heartbeat_at=now() WHERE id='`+nodes[0]+`'`,
		`UPDATE nodes SET serving_status='disabled' WHERE id='`+nodes[2]+`'`)
	expect("?state=online", 1, 1)
	expect("?state=offline", 1, 1)
	expect("?state=disabled", 1, 1)
	expect("?state=retired", 0, 0)
	expect("?state=all&q=step3", 3, 3)
	expect("?state=offline&limit=1&offset=5", 0, 1)

	// 列表不带编辑字段；按 id 单取才带（protocol_config 已抹敏感键）
	for _, r := range expect("", 3, 3) {
		for _, k := range []string{"protocol_config", "kernel", "traffic_rate", "traffic_bytes"} {
			if _, ok := r[k]; ok {
				t.Fatalf("list row carries %q", k)
			}
		}
	}
	step3Seed(t, ctx, admin, `UPDATE nodes SET protocol_config='{"flow":"xtls-rprx-vision","password":"pg18-test-only"}'::jsonb WHERE id='`+nodes[1]+`'`)
	one := expect("?id="+nodes[1], 1, 1)[0]
	var cfg map[string]any
	if err := json.Unmarshal(one["protocol_config"], &cfg); err != nil || cfg["flow"] != "xtls-rprx-vision" || cfg["password"] == "pg18-test-only" {
		t.Fatalf("detail protocol_config=%s err=%v", one["protocol_config"], err)
	}
	if string(one["kernel"]) != `"auto"` || string(one["traffic_rate"]) != "1" || one["delivery_note"] == nil {
		t.Fatalf("detail row: kernel=%s traffic_rate=%s delivery_note=%s", one["kernel"], one["traffic_rate"], one["delivery_note"])
	}
	expect("?id=7e000000-0000-4000-8000-0000000000ff", 0, 0)
}
