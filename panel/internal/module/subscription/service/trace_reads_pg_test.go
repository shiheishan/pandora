package service

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/google/uuid"
)

// traceIPv6 生成本用例专用的文档段来源 IP（2001:db8::/32），避开库里别的用例的行。
func traceIPv6(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("2001:db8::%x:%x", binary.BigEndian.Uint16(b[:2]), binary.BigEndian.Uint16(b[2:]))
}

// traceFixture 造两个用户 A、B 各一份订阅，写入拉取日志，返回用户 id 与三个来源 IP：
// A 从 ip1 拉 3 次（两种 UA）、从 ip2 拉 1 次失败；B 从 ip1 拉 1 次；未认证 not_found 从 ip3 记 1 次。
func traceFixture(t *testing.T, de *deliveryEnv, ctx context.Context) (a, b uuid.UUID, ip1, ip2, ip3 string) {
	t.Helper()
	a = newUser(t, de.env, ctx)
	b = newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	subA := provision(t, de.env, ctx, a, planID, versionID, "A")
	subB := provision(t, de.env, ctx, b, planID, versionID, "B")

	var credA, credB uuid.UUID
	if err := de.row(ctx, `SELECT id FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, subA.ID).Scan(&credA); err != nil {
		t.Fatalf("读 A 的凭据: %v", err)
	}
	if err := de.row(ctx, `SELECT id FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, subB.ID).Scan(&credB); err != nil {
		t.Fatalf("读 B 的凭据: %v", err)
	}

	ip1, ip2, ip3 = traceIPv6(t), traceIPv6(t), traceIPv6(t)
	d := de.svc.delivery

	// A 从 ip1 拉 3 次，两种 UA；再从 ip2 拉 1 次失败
	for _, ua := range []string{"ClashForWindows/0.20.39", "ClashForWindows/0.20.39", "ClashX/1.9.0"} {
		de.svc.logFetch(pullCtx(ctx, ip1, ua), d, fetchEntry{credID: &credA, subID: &subA.ID, result: FetchOK, family: "clash"})
	}
	de.svc.logFetch(pullCtx(ctx, ip2, "ClashForWindows/0.20.39"), d,
		fetchEntry{credID: &credA, subID: &subA.ID, result: FetchError, family: "clash"})
	// B 从 ip1 拉 1 次，UA 与 A 的一种相同
	de.svc.logFetch(pullCtx(ctx, ip1, "ClashX/1.9.0"), d, fetchEntry{credID: &credB, subID: &subB.ID, result: FetchOK, family: "clash"})
	// 未认证失败（没有凭据与订阅）从 ip3 记 1 次
	de.svc.logFetch(pullCtx(ctx, ip3, "Go-http-client/2.0"), d, fetchEntry{result: FetchNotFound, family: "other"})

	return a, b, ip1, ip2, ip3
}

func TestTraceFetchIPsPG(t *testing.T) {
	de := newDeliveryEnv(t, false)
	ctx := de.txCtx()
	a, b, ip1, ip2, ip3 := traceFixture(t, de, ctx)

	set, err := de.svc.TraceFetchIPs(ctx, identitysvc.TraceFetchIPQuery{
		Since:    time.Now().Add(-time.Hour),
		Limit:    0,
		ExtraIPs: []string{ip1, ip2, ip3},
	})
	if err != nil {
		t.Fatalf("按 IP 聚合: %v", err)
	}
	if set.Rows == nil {
		t.Fatal("Rows 不应为 nil")
	}
	if set.DistinctIPs < 3 {
		t.Fatalf("DistinctIPs = %d，至少 3", set.DistinctIPs)
	}
	if set.Fetches < 6 {
		t.Fatalf("Fetches = %d，至少 6", set.Fetches)
	}

	byIP := make(map[string]identitysvc.TraceFetchIP, len(set.Rows))
	for _, r := range set.Rows {
		byIP[r.IP] = r
	}

	row1, ok := byIP[ip1]
	if !ok {
		t.Fatalf("结果里没有 ip1(%s)：%+v", ip1, set.Rows)
	}
	if row1.Fetches != 4 {
		t.Errorf("ip1 Fetches = %d，期望 4", row1.Fetches)
	}
	if row1.Accounts != 2 {
		t.Errorf("ip1 Accounts = %d，期望 2", row1.Accounts)
	}
	if !containsID(row1.AccountIDs, a) || !containsID(row1.AccountIDs, b) {
		t.Errorf("ip1 AccountIDs = %v，应含 A、B", row1.AccountIDs)
	}
	if row1.UAKinds != 2 {
		t.Errorf("ip1 UAKinds = %d，期望 2", row1.UAKinds)
	}
	if row1.Failures != 0 {
		t.Errorf("ip1 Failures = %d，期望 0", row1.Failures)
	}

	row2, ok := byIP[ip2]
	if !ok {
		t.Fatalf("结果里没有 ip2(%s)", ip2)
	}
	if row2.Failures != 1 || row2.Fetches != 1 {
		t.Errorf("ip2 = %d 次 / %d 失败，期望 1 / 1", row2.Fetches, row2.Failures)
	}

	row3, ok := byIP[ip3]
	if !ok {
		t.Fatalf("结果里没有 ip3(%s)", ip3)
	}
	if row3.Accounts != 0 {
		t.Errorf("ip3 Accounts = %d，期望 0", row3.Accounts)
	}
	if row3.Failures != 1 || row3.Fetches != 1 {
		t.Errorf("ip3 = %d 次 / %d 失败，期望 1 / 1", row3.Fetches, row3.Failures)
	}
}

func TestTraceFetchUsersPG(t *testing.T) {
	de := newDeliveryEnv(t, false)
	ctx := de.txCtx()
	a, b, ip1, ip2, _ := traceFixture(t, de, ctx)

	users, err := de.svc.TraceFetchUsers(ctx, identitysvc.TraceFetchUserQuery{
		Since:   time.Now().Add(-time.Hour),
		Sort:    "fetches",
		Limit:   0,
		UserIDs: []uuid.UUID{a, b},
	})
	if err != nil {
		t.Fatalf("按用户聚合: %v", err)
	}
	if users == nil {
		t.Fatal("结果不应为 nil")
	}

	ua, ok := findTraceUser(users, a)
	if !ok {
		t.Fatalf("结果里没有用户 A：%+v", users)
	}
	if ua.Fetches != 4 {
		t.Errorf("A Fetches = %d，期望 4", ua.Fetches)
	}
	if ua.Failures != 1 {
		t.Errorf("A Failures = %d，期望 1", ua.Failures)
	}
	if ua.DistinctIPs != 2 {
		t.Errorf("A DistinctIPs = %d，期望 2", ua.DistinctIPs)
	}
	if ua.SharedIPs != 1 {
		t.Errorf("A SharedIPs = %d，期望 1", ua.SharedIPs)
	}
	if len(ua.Families) != 1 || ua.Families[0] != "clash" {
		t.Errorf("A Families = %v，期望 [clash]", ua.Families)
	}
	if len(ua.IPs) != 2 {
		t.Fatalf("A IPs = %+v，期望 2 个", ua.IPs)
	}
	if ua.IPs[0].IP != ip1 || ua.IPs[0].Count != 3 || !ua.IPs[0].Shared {
		t.Errorf("A IPs[0] = %+v，期望 {%s 3 true}", ua.IPs[0], ip1)
	}
	if ua.IPs[1].IP != ip2 || ua.IPs[1].Count != 1 || ua.IPs[1].Shared {
		t.Errorf("A IPs[1] = %+v，期望 {%s 1 false}", ua.IPs[1], ip2)
	}

	ub, ok := findTraceUser(users, b)
	if !ok {
		t.Fatalf("结果里没有用户 B：%+v", users)
	}
	if ub.Fetches != 1 {
		t.Errorf("B Fetches = %d，期望 1", ub.Fetches)
	}
	if ub.SharedIPs != 1 {
		t.Errorf("B SharedIPs = %d，期望 1", ub.SharedIPs)
	}

	empty, err := de.svc.TraceFetchUsers(ctx, identitysvc.TraceFetchUserQuery{
		Since:   time.Now().Add(time.Hour),
		Sort:    "fetches",
		UserIDs: []uuid.UUID{a, b},
	})
	if err != nil {
		t.Fatalf("窗外的按用户聚合: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("窗外的结果应为空，得到 %+v", empty)
	}
}

// containsID 报告 ids 里有没有 id。
func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// findTraceUser 在结果里找用户。
func findTraceUser(users []identitysvc.TraceFetchUser, id uuid.UUID) (identitysvc.TraceFetchUser, bool) {
	for _, u := range users {
		if u.UserID == id {
			return u, true
		}
	}
	return identitysvc.TraceFetchUser{}, false
}
