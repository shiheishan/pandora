package subscription

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 后台的「是否下发」要和订阅下载同口径：DeliveryState 判下发的节点，若服务器没
// 就绪、协议或地址不全、池没绑任何套餐，实际一个用户都拿不到，界面必须说不下发。
func TestNodeDeliveryFactsRefine(t *testing.T) {
	all := NodeDeliveryFacts{Deliverable: true, ServerReady: true, PoolBound: true}
	for _, tc := range []struct {
		name      string
		facts     NodeDeliveryFacts
		delivered bool
		note      string
		want      bool
		wantNote  string
	}{
		{"全部满足时原样返回", all, true, "", true, ""},
		{"超时保底的说明保留", all, true, "心跳已超时", true, "心跳已超时"},
		{"DeliveryState 已说不下发的原样返回", NodeDeliveryFacts{}, false, "未划入节点池，不服务任何用户", false, "未划入节点池，不服务任何用户"},
		{"服务器未就绪", NodeDeliveryFacts{PoolBound: true}, true, "", false, ServerNotReadyNote},
		{"服务器就绪但协议或地址不全", NodeDeliveryFacts{ServerReady: true, PoolBound: true}, true, "", false, NodeNotReadyNote},
		{"池没绑任何套餐", NodeDeliveryFacts{Deliverable: true, ServerReady: true}, true, "", false, PoolUnboundNote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, note := tc.facts.Refine(tc.delivered, tc.note)
			if got != tc.want || note != tc.wantNote {
				t.Fatalf("Refine = %v %q，期望 %v %q", got, note, tc.want, tc.wantNote)
			}
		})
	}
}

// 节点自身的资格条件只有 DeliverableNodeSQL 一份：订阅下载、套餐页的可下发节点数、
// 后台节点列表的下发说明都引用它，不各写一份。
func TestDeliverableNodeSQLIsTheSharedNodeRule(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	frag := pkg.Decl("DeliverableNodeSQL")
	for _, want := range []string{
		`(s.control_node_id IS DISTINCT FROM n.id OR n.status = 'active')`,
		`AND n.node_type IS NOT NULL`,
		`AND n.server_port BETWEEN 1 AND 65535`,
		`AND s.status = 'ready' AND s.deleted_at IS NULL`,
		`AND n.serving_status = 'active'`,
		`AND n.last_heartbeat_at IS NOT NULL`,
		`nodefabric.StableProtocolReadySQL("n")`,
		"nodeHostSQL + ` <> ''",
	} {
		if !strings.Contains(frag, want) {
			t.Fatalf("DeliverableNodeSQL 缺少条件 %q", want)
		}
	}
	if strings.Count(pkg.Source(), "DeliverableNodeSQL()") < 2 ||
		!strings.Contains(pkg.Decl("listEligibleNodesTx"), "`+DeliverableNodeSQL()+`") ||
		!strings.Contains(pkg.Decl("listEligibleNodesTx"), "`+nodeHostSQL+`") {
		t.Fatal("订阅资格查询必须引用 DeliverableNodeSQL 与 nodeHostSQL")
	}
	if !strings.Contains(sourcetest.Load(t, "../adminops").Decl("Service.PlanPools"), "subscription.DeliverableNodeSQL()") {
		t.Fatal("套餐页的可下发节点数必须按 subscription.DeliverableNodeSQL 计数")
	}
	nodeList := sourcetest.Load(t, "../../api/admin").Decl("handlers.nodeList")
	if !strings.Contains(nodeList, "subscription.NodeDeliverability(") || !strings.Contains(nodeList, ".Refine(") {
		t.Fatal("节点列表的下发说明必须经 NodeDeliverability 与 Refine 补齐")
	}
}

// inet::text 带掩码（203.0.113.7/32），写进订阅就是错地址；回落地址只能用 host()。
func TestNodeHostFallbackStripsInetMask(t *testing.T) {
	if !strings.Contains(nodeHostSQL, "host(n.public_ipv4)") {
		t.Fatalf("回落地址必须用 host() 取纯地址：%s", nodeHostSQL)
	}
	src := sourcetest.Load(t, ".").Source()
	if strings.Contains(src, "public_ipv4::text") || strings.Contains(src, "public_ipv6::text") {
		t.Fatal("订阅包里不许把 inet 直接转 text：会带上 /32、/128 掩码")
	}
}
