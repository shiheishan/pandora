package service

// 名单（Rosters）与订阅线路（DeliverableNodes）在同一份夹具上对照：同一套资格规则，两边结论一致；
// 只有节点侧条件不同的地方（服务器排空中名单有线路无、心跳分层只作用于线路）分开断言。

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

type deliveryFixture struct {
	gA, gB                         uuid.UUID
	s1, s2, s3, s4                 uuid.UUID
	n1, n2, n3, n4, n5, n6, n7, n8 uuid.UUID
	v                              uuid.UUID
	// 订阅（按用户）
	uA, uB, uD, uOverride, uPack, uExhausted, uExpired, uPastEnd, uBanned, uNoQuota fixtureSub
	// 订阅主人
	userA, userB, userD uuid.UUID
	// 节点 → 线路名
	names map[uuid.UUID]string
	ports map[uuid.UUID]int
}

func seedDeliveryFixture(t *testing.T, ctx context.Context, e *deliveryEnv) *deliveryFixture {
	t.Helper()
	f := &deliveryFixture{names: map[uuid.UUID]string{}, ports: map[uuid.UUID]int{}}
	f.gA, f.gB = e.dGroup(t, ctx), e.dGroup(t, ctx)

	p1 := e.dPool(t, ctx, PoolActive)
	p2 := e.dPool(t, ctx, PoolActive, f.gA)
	p3 := e.dPool(t, ctx, PoolDisabled)
	p4 := e.dPool(t, ctx, PoolActive)

	f.s1 = e.dServer(t, ctx, "ready", ptrDur(time.Minute))
	f.s2 = e.dServer(t, ctx, "draining", ptrDur(time.Minute))
	f.s3 = e.dServer(t, ctx, "ready", ptrDur(20*time.Minute))
	f.s4 = e.dServer(t, ctx, "draft", nil)

	node := func(pool *uuid.UUID, server uuid.UUID, serving string, port int, name string) uuid.UUID {
		id := e.dNode(t, ctx, pool, server, serving, port, name)
		f.names[id], f.ports[id] = name, port
		return id
	}
	f.n1 = node(&p1, f.s1, "active", 20001, "n1")
	f.n2 = node(&p2, f.s1, "active", 20002, "n2")
	f.n3 = node(&p3, f.s1, "active", 20003, "n3")
	f.n4 = node(&p4, f.s1, "active", 20004, "n4")
	f.n5 = node(nil, f.s1, "active", 20005, "n5")
	f.n6 = node(&p1, f.s2, "active", 20006, "n6")
	f.n7 = node(&p1, f.s1, "disabled", 20007, "n7")
	f.n8 = node(&p1, f.s3, "active", 20008, "n8")

	f.v = e.dVersion(t, ctx, intPtr(1000), intPtr(3), p1, p2, p3)

	gib := int64(1) << 30
	quota := func(consumed int64) subOpts { return subOpts{QuotaLimit: &gib, QuotaConsumed: consumed} }

	f.userA = e.dUser(t, ctx, "active", &f.gA)
	f.uA = e.dSub(t, ctx, f.userA, f.v, quota(0))
	f.userB = e.dUser(t, ctx, "active", &f.gB)
	f.uB = e.dSub(t, ctx, f.userB, f.v, quota(0))
	f.userD = e.dUser(t, ctx, "active", nil)
	f.uD = e.dSub(t, ctx, f.userD, f.v, quota(0))

	o := quota(0)
	o.DeviceLimit = intPtr(0)
	f.uOverride = e.dSub(t, ctx, e.dUser(t, ctx, "active", &f.gA), f.v, o)
	o = quota(gib)
	o.PackGranted, o.PackConsumed = 100, 10
	f.uPack = e.dSub(t, ctx, e.dUser(t, ctx, "active", &f.gA), f.v, o)
	f.uExhausted = e.dSub(t, ctx, e.dUser(t, ctx, "active", &f.gA), f.v, quota(gib))
	o = quota(0)
	o.Expired = true
	f.uExpired = e.dSub(t, ctx, e.dUser(t, ctx, "active", &f.gA), f.v, o)
	o = quota(0)
	o.PastEnd = true
	f.uPastEnd = e.dSub(t, ctx, e.dUser(t, ctx, "active", &f.gA), f.v, o)
	f.uBanned = e.dSub(t, ctx, e.dUser(t, ctx, "banned", &f.gA), f.v, quota(0))
	f.uNoQuota = e.dSub(t, ctx, e.dUser(t, ctx, "active", &f.gA), f.v, subOpts{})
	return f
}

// rosterUIDs 取名单里的 uid（本用例的池是新建的，只有夹具订阅的版本绑它们）。
func rosterUIDs(r NodeRoster) []int64 {
	out := make([]int64, len(r.Users))
	for i, u := range r.Users {
		out[i] = u.UID
	}
	return out
}

func sortedUIDs(subs ...fixtureSub) []int64 {
	out := make([]int64, len(subs))
	for i, s := range subs {
		out[i] = s.UID
	}
	slices.Sort(out)
	return out
}

func TestRostersPG(t *testing.T) {
	e, ctx := newDeliveryEnv(t)
	f := seedDeliveryFixture(t, ctx, e)

	rosters, err := e.svc.Rosters(ctx, []uuid.UUID{f.s1, f.s2, f.s3, f.s4, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rosters) != 4 {
		t.Fatalf("应只有 4 台服务器，得 %d", len(rosters))
	}
	if r, ok := rosters[f.s4]; !ok || r == nil || len(r) != 0 {
		t.Fatalf("草稿服务器应是非 nil 空切片: %v %v", ok, r)
	}

	wantNodes := map[uuid.UUID][]uuid.UUID{
		f.s1: {f.n1, f.n2, f.n3, f.n4, f.n5},
		f.s2: {f.n6},
		f.s3: {f.n8},
	}
	for server, want := range wantNodes {
		slices.SortFunc(want, compareUUID)
		got := make([]uuid.UUID, 0)
		for _, r := range rosters[server] {
			got = append(got, r.NodeID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("服务器 %s 的节点 %v，期望 %v（按 id 升序）", server, got, want)
		}
		for i := 1; i < len(got); i++ {
			if bytes.Compare(got[i-1][:], got[i][:]) >= 0 {
				t.Fatalf("节点未按 id 升序: %v", got)
			}
		}
	}

	all := sortedUIDs(f.uA, f.uB, f.uD, f.uOverride, f.uPack, f.uNoQuota)
	onlyA := sortedUIDs(f.uA, f.uOverride, f.uPack, f.uNoQuota)
	r1, _ := rosterOf(rosters, f.s1, f.n1)
	if got := rosterUIDs(r1); !slices.Equal(got, all) {
		t.Fatalf("n1 名单 %v，期望 %v", got, all)
	}
	if u, ok := hasUID(r1, f.uA.UID); !ok || u.UUID != f.uA.Proxy || u.SpeedLimitKbps != 1000 || u.DeviceLimit != 3 {
		t.Fatalf("uA 名单项 %+v", u)
	}
	if u, _ := hasUID(r1, f.uOverride.UID); u.DeviceLimit != 0 || u.SpeedLimitKbps != 1000 {
		t.Fatalf("设备覆盖 0 的名单项 %+v", u)
	}
	r2, _ := rosterOf(rosters, f.s1, f.n2)
	if got := rosterUIDs(r2); !slices.Equal(got, onlyA) {
		t.Fatalf("n2（限定 gA）名单 %v，期望 %v", got, onlyA)
	}
	r3, _ := rosterOf(rosters, f.s1, f.n3)
	if got := rosterUIDs(r3); !slices.Equal(got, all) {
		t.Fatalf("n3（停用的池照常服务）名单 %v，期望 %v", got, all)
	}
	for _, n := range []uuid.UUID{f.n4, f.n5} {
		r, _ := rosterOf(rosters, f.s1, n)
		if r.Users == nil || len(r.Users) != 0 || r.Version != rosterVersion(nil) {
			t.Fatalf("节点 %s 应是空名单: %+v", f.names[n], r)
		}
	}
	r6, _ := rosterOf(rosters, f.s2, f.n6)
	r8, _ := rosterOf(rosters, f.s3, f.n8)
	if r1.Version != r6.Version || r1.Version != r8.Version {
		t.Fatal("同池节点的版本应相同")
	}
	if r1.Version == r2.Version {
		t.Fatal("不同内容的名单版本应不同")
	}
	if r1.Version != rosterVersion(r1.Users) {
		t.Fatal("版本应等于名单内容的哈希")
	}
}

func TestDeliverableNodesPG(t *testing.T) {
	e, ctx := newDeliveryEnv(t)
	f := seedDeliveryFixture(t, ctx, e)

	cases := []struct {
		name  string
		sub   fixtureSub
		user  uuid.UUID
		group *uuid.UUID
		want  []string
	}{
		{"gA", f.uA, f.userA, &f.gA, []string{"n1", "n2", "n3"}},
		{"gB", f.uB, f.userB, &f.gB, []string{"n1", "n3"}},
		{"默认组", f.uD, f.userD, nil, []string{"n1", "n3"}},
	}
	for _, c := range cases {
		nodes, err := e.svc.DeliverableNodes(ctx, subsvc.NodeQuery{
			SubscriptionID: c.sub.ID, UserID: c.user, PlanVersionID: f.v, UserGroupID: c.group,
		})
		if err != nil {
			t.Fatal(err)
		}
		got := lineNames(nodes)
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Fatalf("%s 线路 %v，期望 %v", c.name, got, c.want)
		}
		for _, n := range nodes {
			if n.Type != "vless" || n.Host != "node.example.test" || n.TrafficRate != 1 || n.Config == nil {
				t.Fatalf("线路字段 %+v", n)
			}
			if _, ok := n.Config["flow"]; !ok {
				t.Fatalf("协议配置缺 flow: %v", n.Config)
			}
		}
	}
	nodes, _ := e.svc.DeliverableNodes(ctx, subsvc.NodeQuery{PlanVersionID: f.v, UserGroupID: &f.gA})
	for _, n := range nodes {
		want := map[string]int{"n1": 20001, "n2": 20002, "n3": 20003}[n.Name]
		if n.Port != want {
			t.Fatalf("%s 端口 %d，期望 %d", n.Name, n.Port, want)
		}
	}
}

// TestRosterAndLinesAgreePG：节点侧条件相同的节点上，名单里有这个用户 ⇔ 这个用户的线路里有这个节点。
func TestRosterAndLinesAgreePG(t *testing.T) {
	e, ctx := newDeliveryEnv(t)
	f := seedDeliveryFixture(t, ctx, e)
	rosters, err := e.svc.Rosters(ctx, []uuid.UUID{f.s1})
	if err != nil {
		t.Fatal(err)
	}
	users := []struct {
		sub   fixtureSub
		user  uuid.UUID
		group *uuid.UUID
	}{{f.uA, f.userA, &f.gA}, {f.uB, f.userB, &f.gB}, {f.uD, f.userD, nil}}
	for _, u := range users {
		nodes, err := e.svc.DeliverableNodes(ctx, subsvc.NodeQuery{
			SubscriptionID: u.sub.ID, UserID: u.user, PlanVersionID: f.v, UserGroupID: u.group,
		})
		if err != nil {
			t.Fatal(err)
		}
		lines := lineNames(nodes)
		for _, n := range []uuid.UUID{f.n1, f.n2, f.n3, f.n4, f.n5, f.n7} {
			r, _ := rosterOf(rosters, f.s1, n)
			_, inRoster := hasUID(r, u.sub.UID)
			inLines := slices.Contains(lines, f.names[n])
			if inRoster != inLines {
				t.Fatalf("用户 %d 节点 %s：名单 %v、线路 %v 不一致", u.sub.UID, f.names[n], inRoster, inLines)
			}
		}
	}
}

func TestDeliverableNodesTiersPG(t *testing.T) {
	e, ctx := newDeliveryEnv(t)
	s1 := e.dServer(t, ctx, "ready", ptrDur(time.Minute))
	s3 := e.dServer(t, ctx, "ready", ptrDur(20*time.Minute))
	p5 := e.dPool(t, ctx, PoolActive)
	n9 := e.dNode(t, ctx, &p5, s1, "active", 21009, "n9")
	n10 := e.dNode(t, ctx, &p5, s3, "active", 21010, "n10")
	v2 := e.dVersion(t, ctx, nil, nil, p5)
	q := subsvc.NodeQuery{PlanVersionID: v2}

	check := func(label string, want ...string) {
		t.Helper()
		nodes, err := e.svc.DeliverableNodes(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		got := lineNames(nodes)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: 线路 %v，期望 %v", label, got, want)
		}
	}

	e.mustExec(t, ctx, `UPDATE nodes SET runtime_status = 'degraded', runtime_state_at = now() WHERE id = ?`, n9)
	check("先摘降级", "n10")
	e.mustExec(t, ctx, `UPDATE nodes SET apply_failed_sha256 = repeat('a', 64), apply_failed_reason = 'x',
		apply_failed_at = now() WHERE id = ?`, n10)
	check("全降级照发，再分新鲜", "n9")
	e.mustExec(t, ctx, `UPDATE nodes SET runtime_status = 'down' WHERE id = ?`, n9)
	e.mustExec(t, ctx, `UPDATE servers SET last_heartbeat_at = now() - interval '30 minutes' WHERE id = ?`, s1)
	check("全降级全超时全给", "n9", "n10")
	e.mustExec(t, ctx, `UPDATE nodes SET runtime_status = 'running' WHERE id = ?`, n9)
	check("恢复后只给不降级的", "n9")
}

func TestRostersEmptyAndNoBindingPG(t *testing.T) {
	e, ctx := newDeliveryEnv(t)
	out, err := e.svc.Rosters(ctx, nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("空输入: %v %v", out, err)
	}
	v := e.dVersion(t, ctx, nil, nil)
	nodes, err := e.svc.DeliverableNodes(ctx, subsvc.NodeQuery{PlanVersionID: v})
	if err != nil {
		t.Fatal(err)
	}
	if nodes == nil || len(nodes) != 0 {
		t.Fatalf("没绑池的版本应是非 nil 空切片: %v", nodes)
	}
}

func TestUsersChangedBumpsEpoch(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	svc := New(nil, nil, nil, rdb)
	svc.UsersChanged(context.Background(), nil)
	svc.UsersChanged(context.Background(), []uuid.UUID{uuid.New()})
	if v, err := mr.Get(UsersEpochKey); err != nil || v != "2" {
		t.Fatalf("名单纪元 %q %v，期望 2", v, err)
	}
}
