package service

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

func randomDocIPv6(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	r1 := binary.BigEndian.Uint16(b[0:2])
	r2 := binary.BigEndian.Uint16(b[2:4])
	// 经 netip 转成规范写法：段为 0 时 %x 会拼出「::0:5」这类非规范串，入库哈希就和补查对不上
	return netip.MustParseAddr(fmt.Sprintf("2001:db8::%x:%x", r1, r2)).String()
}

func userCtxUA(ctx context.Context, user uuid.UUID, ip, ua string) context.Context {
	ctx = reqid.With(ctx, reqid.New())
	ctx = authn.With(ctx, authn.Principal{
		UserID:    user,
		SessionID: "s1",
		Audience:  authn.AudiencePublic,
	})
	return reqmeta.With(ctx, reqmeta.Meta{IP: ip, UserAgent: ua})
}

func anonCtxIP(ctx context.Context, ip string) context.Context {
	ctx = reqid.With(ctx, reqid.New())
	return reqmeta.With(ctx, reqmeta.Meta{IP: ip, UserAgent: "anon-ua/1"})
}

func findTraceIP(rows []TraceIP, ip string) *TraceIP {
	for i := range rows {
		if rows[i].IP == ip {
			return &rows[i]
		}
	}
	return nil
}

func findTraceUser(rows []TraceUser, id uuid.UUID) *TraceUser {
	for i := range rows {
		if rows[i].UserID == id {
			return &rows[i]
		}
	}
	return nil
}

func traceUserIndex(rows []TraceUser, id uuid.UUID) int {
	for i := range rows {
		if rows[i].UserID == id {
			return i
		}
	}
	return -1
}

func traceIPIndex(rows []TraceIP, ip string) int {
	for i := range rows {
		if rows[i].IP == ip {
			return i
		}
	}
	return -1
}

func uuidInList(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func seedTraceReadsData(t *testing.T, ctx context.Context, svc *Service, A, B uuid.UUID, ip1, ip2 string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		ua := "ua-trace-a/1"
		if i == 1 {
			ua = "ua-trace-a/2"
		}
		if err := svc.Write(userCtxUA(ctx, A, ip1, ua), Event{Action: "user.login"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		ua := "ua-trace-b/1"
		if i == 1 {
			ua = "ua-trace-b/2"
		}
		if err := svc.Write(userCtxUA(ctx, B, ip1, ua), Event{Action: "user.login"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Write(userCtx(ctx, A, ip2), Event{Action: "user.login"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Write(anonCtxIP(ctx, ip1), Event{Action: "user.login_failed"}); err != nil {
		t.Fatal(err)
	}
}

func TestTraceIPsPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	ip1 := randomDocIPv6(t)
	ip2 := randomDocIPv6(t)
	A, B := uuid.New(), uuid.New()
	seedTraceReadsData(t, ctx, svc, A, B, ip1, ip2)

	since := time.Now().Add(-time.Hour)
	set, err := svc.TraceIPs(ctx, TraceIPQuery{Since: since, Limit: 0, ExtraIPs: []string{ip1, ip2}})
	if err != nil {
		t.Fatal(err)
	}
	if set.Rows == nil {
		t.Fatal("Rows 应为非 nil 切片")
	}
	row1 := findTraceIP(set.Rows, ip1)
	if row1 == nil {
		t.Fatalf("应能找到 IP1 %s", ip1)
	}
	if row1.Events != 5 || row1.Accounts != 2 {
		t.Fatalf("IP1 统计 %+v", *row1)
	}
	if !uuidInList(row1.AccountIDs, A) || !uuidInList(row1.AccountIDs, B) {
		t.Fatalf("IP1 AccountIDs %v", row1.AccountIDs)
	}
	row2 := findTraceIP(set.Rows, ip2)
	if row2 == nil {
		t.Fatalf("应能找到 IP2 %s", ip2)
	}
	if row2.Events != 1 || row2.Accounts != 1 {
		t.Fatalf("IP2 统计 %+v", *row2)
	}
	if set.DistinctIPs != 0 {
		t.Fatalf("Limit 0 窄 SQL 不算全窗总数，DistinctIPs 应为 0，得到 %d", set.DistinctIPs)
	}
	if set.Logins != 0 {
		t.Fatalf("Limit 0 时 Logins 应为 0，得到 %d", set.Logins)
	}
	if set.LoginFailures != 0 {
		t.Fatalf("Limit 0 时 LoginFailures 应为 0，得到 %d", set.LoginFailures)
	}

	future := time.Now().Add(time.Hour)
	setEmpty, err := svc.TraceIPs(ctx, TraceIPQuery{Since: future, Limit: 0, ExtraIPs: []string{ip1}})
	if err != nil {
		t.Fatal(err)
	}
	if findTraceIP(setEmpty.Rows, ip1) != nil {
		t.Fatal("未来 Since 不应含 IP1")
	}
}

func TestTraceIPsHeadPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	ip1 := randomDocIPv6(t)
	ip2 := randomDocIPv6(t)
	A, B := uuid.New(), uuid.New()
	seedTraceReadsData(t, ctx, svc, A, B, ip1, ip2)

	since := time.Now().Add(-time.Hour)
	set, err := svc.TraceIPs(ctx, TraceIPQuery{Since: since, Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if findTraceIP(set.Rows, ip1) == nil {
		t.Fatalf("应能找到 ip1 %s", ip1)
	}
	if findTraceIP(set.Rows, ip2) == nil {
		t.Fatalf("应能找到 ip2 %s", ip2)
	}
	if traceIPIndex(set.Rows, ip1) >= traceIPIndex(set.Rows, ip2) {
		t.Fatalf("ip1 事件更多应排在 ip2 前：ip1@%d ip2@%d", traceIPIndex(set.Rows, ip1), traceIPIndex(set.Rows, ip2))
	}
	if set.DistinctIPs < 2 {
		t.Fatalf("DistinctIPs %d，至少 2", set.DistinctIPs)
	}
	if set.Logins < 5 {
		t.Fatalf("Logins %d，至少 5（A 3 + B 2）", set.Logins)
	}
	if set.LoginFailures < 1 {
		t.Fatalf("LoginFailures %d，至少 1", set.LoginFailures)
	}
}

func assertTraceUserAStats(t *testing.T, u *TraceUser) {
	t.Helper()
	if u.Logins != 3 || u.LoginIPs != 2 || u.DistinctIPs != 2 || u.SharedIPs != 1 {
		t.Fatalf("A 统计 %+v", *u)
	}
	if len(u.IPs) != 2 {
		t.Fatalf("A IPs 长度 %d，期望 2", len(u.IPs))
	}
}

func TestTraceUsersSortsPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	ip1 := randomDocIPv6(t)
	ip2 := randomDocIPv6(t)
	A, B := uuid.New(), uuid.New()
	seedTraceReadsData(t, ctx, svc, A, B, ip1, ip2)

	since := time.Now().Add(-time.Hour)
	for _, key := range []string{"ips", "logins", "login_ips", "shared_ips", "ua_kinds"} {
		t.Run(key, func(t *testing.T) {
			users, err := svc.TraceUsers(ctx, TraceUserQuery{Since: since, Sort: key, Limit: 500})
			if err != nil {
				t.Fatal(err)
			}
			uA := findTraceUser(users, A)
			uB := findTraceUser(users, B)
			if uA == nil || uB == nil {
				t.Fatalf("A、B 都应存在，得到 %d 行", len(users))
			}
			assertTraceUserAStats(t, uA)
			iA, iB := traceUserIndex(users, A), traceUserIndex(users, B)
			switch key {
			case "ips", "logins", "login_ips":
				if iA >= iB {
					t.Fatalf("%s：A 应排在 B 前，A@%d B@%d", key, iA, iB)
				}
			case "ua_kinds":
				// A：ua-trace-a/1、ua-trace-a/2、userCtx 的 ua-test/1；B：ua-trace-b/1、ua-trace-b/2
				if uA.UAKinds > uB.UAKinds && iA >= iB {
					t.Fatalf("ua_kinds：A 种类更多时应排在 B 前，A=%d B=%d，A@%d B@%d", uA.UAKinds, uB.UAKinds, iA, iB)
				}
			case "shared_ips":
				if uA.SharedIPs != 1 || uB.SharedIPs != 1 {
					t.Fatalf("shared_ips：两人 SharedIPs 都应为 1，A=%d B=%d", uA.SharedIPs, uB.SharedIPs)
				}
			}
		})
	}
}

func TestTraceUsersPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	ip1 := randomDocIPv6(t)
	ip2 := randomDocIPv6(t)
	A, B := uuid.New(), uuid.New()
	seedTraceReadsData(t, ctx, svc, A, B, ip1, ip2)

	since := time.Now().Add(-time.Hour)
	users, err := svc.TraceUsers(ctx, TraceUserQuery{Since: since, Limit: 0, UserIDs: []uuid.UUID{A, B}})
	if err != nil {
		t.Fatal(err)
	}
	if users == nil {
		t.Fatal("users 应为非 nil 切片")
	}
	uA := findTraceUser(users, A)
	if uA == nil {
		t.Fatal("应能找到用户 A")
	}
	if uA.Logins != 3 || uA.LoginIPs != 2 || uA.DistinctIPs != 2 || uA.SharedIPs != 1 {
		t.Fatalf("A 统计 %+v", *uA)
	}
	if uA.IPs == nil || len(uA.IPs) != 2 {
		t.Fatalf("A IPs %+v", uA.IPs)
	}
	if uA.IPs[0].IP != ip1 || uA.IPs[0].Count != 2 || !uA.IPs[0].Shared {
		t.Fatalf("A IPs[0] %+v", uA.IPs[0])
	}
	if uA.IPs[1].IP != ip2 || uA.IPs[1].Shared {
		t.Fatalf("A IPs[1] %+v", uA.IPs[1])
	}
	uB := findTraceUser(users, B)
	if uB == nil {
		t.Fatal("应能找到用户 B")
	}
	if uB.SharedIPs != 1 {
		t.Fatalf("B SharedIPs %d", uB.SharedIPs)
	}
}
