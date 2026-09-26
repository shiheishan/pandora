// [INPUT]: 依赖 preferFreshNodes、DeliveryState，依赖 platform/sourcetest 按名取 listEligibleNodesTx、api/admin 的 handlers.nodeList 与两个包的全部源码
// [OUTPUT]: 对外提供 TestPreferFreshNodes、TestDeliveryStateMatchesEligibilitySQL、TestAdminDoesNotComputeHeartbeatInSQL
// [POS]: subscription 心跳分层下发与后台 DeliveryState 不漂移；后台节点列表不在 SQL 里算心跳，否定检查先确认目标函数存在、再覆盖整个包
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 从未心跳的节点不能下发，心跳超时的节点不能因此让订阅变空。
//
// 起因：生产上 4 个 serving_status='active' 的节点里有 2 个从未上报过
// 心跳（建了没装 agent 的测试残留），订阅照发。付费用户拿到 4 条线路，
// 一半是死的。
//
// 这里锁两件事：
//
//  1. 分层规则本身 —— 有新鲜节点时只给新鲜的，一个都没有时把手上的
//     都给出去，而不是给空列表。
//  2. 后台的 DeliveryState 和 SQL 里的下发规则不漂移。同一个规则写在
//     两处、改了一处，是这类问题最常见的死法。
func TestPreferFreshNodes(t *testing.T) {
	fresh := func(name string) Node { return Node{Name: name, HeartbeatFresh: true} }
	stale := func(name string) Node { return Node{Name: name, HeartbeatFresh: false} }

	for _, tc := range []struct {
		name string
		in   []Node
		want []string
	}{
		{"混合时只给新鲜的", []Node{fresh("a"), stale("b"), fresh("c")}, []string{"a", "c"}},
		{"全新鲜时全给", []Node{fresh("a"), fresh("b")}, []string{"a", "b"}},
		{
			// 这一条是关键：宁可给可能连不上的线路，也不能给空订阅。
			// 客户端拿到空列表会把服务器全清掉，用户从「有几条线路可能
			// 不通」变成「一条都没有」。
			"全超时时保底给出去，而不是清空",
			[]Node{stale("a"), stale("b")},
			[]string{"a", "b"},
		},
		{"本来就没有节点", []Node{}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := preferFreshNodes(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("要 %v，得到 %d 个", tc.want, len(got))
			}
			for i, name := range tc.want {
				if got[i].Name != name {
					t.Fatalf("第 %d 个要 %s，得到 %s", i, name, got[i].Name)
				}
			}
		})
	}
}

func TestDeliveryStateMatchesEligibilitySQL(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	// 资格查询先要存在，下面的正向断言都落在它身上
	body := pkg.Decl("listEligibleNodesTx")

	// SQL 必须排除从未心跳的节点。DeliveryState 对同样的输入也必须说不发；
	// 少了这一条，后台会显示「在下发」而实际不发，运营查不出问题在哪。
	if !strings.Contains(body, "AND n.last_heartbeat_at IS NOT NULL") {
		t.Error("资格查询必须排除从未心跳过的节点")
	}
	if ok, _ := DeliveryState("active", true, false, false); ok {
		t.Error("DeliveryState 说从未心跳的节点会下发，与 SQL 不符")
	}

	// 心跳超时的节点仍然可能被下发（保底路径）。后台若报「不下发」，
	// 管理员会以为用户已经拿不到它了，从而漏掉真正的故障。
	if ok, _ := DeliveryState("active", true, true, false); !ok {
		t.Error("DeliveryState 说超时节点不下发，但保底路径会把它发出去")
	}
	if ok, note := DeliveryState("active", true, true, false); ok && note == "" {
		t.Error("超时但仍在下发的节点必须给出说明，否则界面上只是个没来由的标记")
	}

	// 正常节点不该带告警说明。
	if ok, note := DeliveryState("active", true, true, true); !ok || note != "" {
		t.Errorf("健康节点应无条件下发且无说明，得到 ok=%v note=%q", ok, note)
	}

	// 非 active 的节点 SQL 里就被 serving_status 挡住了。
	if ok, _ := DeliveryState("retired", true, true, true); ok {
		t.Error("retired 节点不该报成会下发")
	}

	// 没划进节点池的节点：SQL 靠 JOIN plan_node_pools 排除（pool_id 为 NULL
	// 连不上），节点用户列表同样一个人都不下发（R104）。心跳再新鲜也不下发。
	if !strings.Contains(body, "ON p.pool_id = n.pool_id") {
		t.Error("资格查询必须经节点池连接套餐授权，无池节点才会被排除")
	}
	if ok, note := DeliveryState("active", false, true, true); ok || note == "" {
		t.Errorf("无池节点必须报不下发并说明原因，得到 ok=%v note=%q", ok, note)
	}

	// 窗口只能有一个出处。两处各写一个 interval 字面量，改了一处就会
	// 出现「后台说在发、实际不发」这种查不出来的偏差。
	if strings.Contains(pkg.Source(), "interval '10 minutes'") {
		t.Error("资格查询不该写死窗口字面量，应使用 HeartbeatFreshWindow")
	}
	if !strings.Contains(body, "HeartbeatFreshWindow.String()") {
		t.Error("资格查询必须使用 HeartbeatFreshWindow 作为窗口来源")
	}
}

// 后台不能把心跳判定放回 SQL 里。
//
// 第一版就是那么写的：SELECT (n.last_heartbeat_at >= now() - $3) AS beat_fresh。
// last_heartbeat_at 为 NULL 时这个表达式求值成 NULL 而不是 false，扫进
// Go 的 bool 直接把整个节点列表打成 500 —— 而且恰恰是在「有从未心跳的
// 节点」时才炸，也就是这次改动本来要处理的那种节点。
//
// 订阅侧同样的表达式没事，因为它的 WHERE 已经把 NULL 行滤掉了；后台要
// 显示全部节点，滤不掉。同一段 SQL 在两个地方安全性不同，这种差别很难
// 靠读代码发现。
//
// 根治办法不是加 COALESCE，而是根本不在 SQL 里算：last_heartbeat_at 已经
// 以 *time.Time 扫出来了，「从未心跳」由 nil 表达得清清楚楚，三值逻辑
// 无从发生。这条测试锁住这个选择。
func TestAdminDoesNotComputeHeartbeatInSQL(t *testing.T) {
	admin := sourcetest.Load(t, "../../api/admin")
	// 先确认节点列表还在、且确实是在 Go 侧判定心跳（下面两条正向断言），再做否定检查；
	// 否定检查覆盖整个 admin 包，节点列表挪到哪个文件都逃不掉
	body := admin.Decl("handlers.nodeList")
	for _, bad := range []string{
		"AS beat_fresh",
		"AS ever_seen",
	} {
		if strings.Contains(admin.Source(), bad) {
			t.Errorf("节点列表不该在 SQL 里算 %q —— NULL 心跳会求值成 NULL 而非 false，"+
				"扫进 bool 会让整个列表 500。改用 LastBeat 指针在 Go 侧判断", bad)
		}
	}

	if !strings.Contains(body, "subscription.DeliveryState(") {
		t.Error("节点列表必须调用 subscription.DeliveryState，不能自己复述一遍下发规则")
	}
	if !strings.Contains(body, "subscription.HeartbeatFreshWindow") {
		t.Error("节点列表必须使用共享的 HeartbeatFreshWindow，不能另写一个窗口")
	}
}
