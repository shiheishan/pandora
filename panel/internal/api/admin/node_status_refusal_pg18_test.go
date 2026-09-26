// [INPUT]: 依赖 delivery_pg18_test.go 的 openDeliveryPG18 与 newDeliveryHarness，依赖 handlers.go 的 nodeSetStatus、nodefabric 的 NodeStatusRefusal，依赖 00005 的状态机触发器与 nodes 表的 CHECK 约束
// [OUTPUT]: 对外提供 TestNodeStatusRefusalPG18
// [POS]: api/admin 的节点状态报错中文化 PG18 门禁（delivery 域，⑪）：后台改状态撞状态机时页面拿到触发器的中文原句；nodes 表能造出来的 CHECK 各造一次，真实约束名都被译成中文、英文原句不进响应
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/db"
)

func TestNodeStatusRefusalPG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant = "93000000-0000-4000-8000-000000000901"
		actor  = "93000000-0000-4000-8000-000000000911"
		server = "93000000-0000-4000-8000-000000000961"
		node   = "93000000-0000-4000-8000-000000000971"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed node refusal fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'node-refusal-pg18','Node Refusal PG18','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'operator@node-refusal.invalid','Operator','active')`, tenant, actor)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'refusal-server','draft')`, tenant, server)
	must(`INSERT INTO nodes(id,tenant_id,name,status,node_type,server_host,server_port,server_id,protocol_schema_version)
		  VALUES($2,$1,'refusal-node','attesting','vless','refusal.invalid',443,$3,1)`, tenant, node, server)

	// 1) 后台改状态撞状态机（attesting 不能直接进 active）：触发器的中文原样给页面
	d := newDeliveryHarness(t, ctx, app, tenant, actor, nil)
	h := &handlers{d: Deps{Pool: app, Node: d.nodes, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	d.router.Post("/v1/nodes/{id}/status", h.nodeSetStatus)
	var version int64
	if err := admin.QueryRow(ctx, `SELECT row_version FROM nodes WHERE id=$1`, node).Scan(&version); err != nil {
		t.Fatal(err)
	}
	w := d.do(http.MethodPost, "/v1/nodes/"+node+"/status",
		`{"status":"active","row_version":`+strconv.FormatInt(version, 10)+`}`)
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || !strings.Contains(body.Error.Message, "非法状态跳转：attesting -> active") {
		t.Fatalf("illegal transition status=%d body=%s", w.Code, w.Body.String())
	}
	t.Log("marker=node_refusal_pg18_trigger_passthrough_ok")

	// 2) nodes 表的 CHECK：这三处 UPDATE 碰不到它们（见 NodeStatusRefusal 的事实核对），
	//    这里用直接 UPDATE 造出真实的数据库错误，证明真实约束名都有中文、原句不外露
	var names []string
	rows, err := admin.Query(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid='public.nodes'::regclass AND contype IN ('c','u') ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	t.Logf("marker=node_refusal_pg18_constraints %s", strings.Join(names, ","))

	cases := []struct {
		name, set, constraint, want string
	}{
		// status 撞不到自己的 CHECK：BEFORE 触发器先拒（约束名为空、文案中文）
		{"status", `status='bogus'`, "", "非法状态跳转"},
		{"serving", `serving_status='bogus'`, "nodes_serving_status_check", "节点服务状态取值不合法"},
		{"desired pair", `desired_effective_generation=1`, "nodes_desired_effective_pair_check", "目标配置版本不完整"},
		{"applied pair", `applied_effective_generation=1`, "nodes_applied_effective_pair_check", "已生效配置不完整"},
		{"hard fault", `hard_fault=true`, "nodes_hard_fault_needs_reason", "硬故障"},
		{"weight", `weight=-1`, "nodes_weight_check", "节点数据不满足数据库约束"},
	}
	for _, tc := range cases {
		_, err := admin.Exec(ctx, `UPDATE nodes SET `+tc.set+` WHERE id=$1`, node)
		if !db.IsCheckViolation(err) || db.ConstraintName(err) != tc.constraint {
			t.Fatalf("%s: err=%v constraint=%q want check violation on %q", tc.name, err, db.ConstraintName(err), tc.constraint)
		}
		got := nodefabric.NodeStatusRefusal(err)
		if !strings.Contains(got.Message, tc.want) || strings.Contains(got.Message, "nodes_") ||
			strings.Contains(got.Message, "violates") {
			t.Fatalf("%s: translated message %q want %q (raw: %s)", tc.name, got.Message, tc.want, db.Message(err))
		}
	}
	t.Log("marker=node_refusal_pg18_constraints_translated_ok")
}
