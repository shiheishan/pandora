package service

// 订阅域名探测的 PG 集成测试（票据与上报走真库 + miniredis 分数）。

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// setupProbe 准备有生效订阅的用户、缺省组两个探测域名、已注入的域名分数 Redis。
func setupProbe(t *testing.T) (*deliveryEnv, context.Context, uuid.UUID, *redis.Client) {
	t.Helper()
	de, ctx, user, _, _ := setupPull(t)
	_, rdb := testkit.Redis(t)
	de.svc.UseDomainScores(rdb)
	admin := adminCtx(ctx, uuid.New())
	_, err := de.svc.AdminSaveDomainConfig(admin, []DomainGroupInput{{
		Name: "缺省", IsDefault: true,
		Domains: []Domain{
			{BaseURL: "https://sub-a.example.test", Weight: 1},
			{BaseURL: "https://sub-b.example.test", Weight: 1},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)
	return de, ctx, user, rdb
}

// seedProbeReporters 往 base 当前小时桶的成功上报人集合里预先放几个别的账号（PG 用例只有一个真实账号，用它凑人数）。
func seedProbeReporters(t *testing.T, rdb *redis.Client, base string, now time.Time, accts ...string) {
	t.Helper()
	members := make([]any, len(accts))
	for i, a := range accts {
		members[i] = a
	}
	if err := rdb.SAdd(context.Background(), probeRedisName(probeOKKeyPrefix, base, probeHourBucket(now)), members...).Err(); err != nil {
		t.Fatal(err)
	}
}

func assertUnauthorized(t *testing.T, err error) {
	t.Helper()
	if errs.As(err).Code != errs.Unauthorized {
		t.Fatalf("要 Unauthorized，得 %v", err)
	}
}

func TestProbeTicketIssueAndFramePG(t *testing.T) {
	de, ctx, user, _ := setupProbe(t)
	uctx := userCtx(ctx, user)

	ticket, err := de.svc.IssueProbeTicket(uctx)
	if err != nil {
		t.Fatal(err)
	}
	if ticket == "" {
		t.Fatal("有生效订阅且组内有域名时应签发非空票据")
	}

	doms, err := de.svc.ProbeFrameDomains(uctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://sub-a.example.test", "https://sub-b.example.test"}
	if len(doms) != len(want) {
		t.Fatalf("域名列表 %v，要 %v", doms, want)
	}
	for i := range want {
		if doms[i] != want[i] {
			t.Fatalf("顺序或值不对：%v，要 %v", doms, want)
		}
	}
}

func TestProbeTicketNoLiveSubscriptionPG(t *testing.T) {
	de, ctx, user, _ := setupProbe(t)

	noSub := newUser(t, de.env, ctx)
	ticket, err := de.svc.IssueProbeTicket(userCtx(ctx, noSub))
	if err != nil {
		t.Fatal(err)
	}
	if ticket != "" {
		t.Fatalf("无订阅应回空串，得 %q", ticket)
	}

	uctx := userCtx(ctx, user)
	liveTicket, err := de.svc.IssueProbeTicket(uctx)
	if err != nil || liveTicket == "" {
		t.Fatalf("先签发: %v %q", err, liveTicket)
	}
	de.exec(t, ctx, `UPDATE subscriptions SET current_period_start = now() - interval '31 days', current_period_end = now() - interval '1 minute' WHERE user_id = ?`, user)
	_, err = de.svc.ProbeFrameDomains(uctx, liveTicket)
	assertUnauthorized(t, err)
}

func TestProbeTicketNoDomainsPG(t *testing.T) {
	de, ctx, user, _, _ := setupPull(t)
	ticket, err := de.svc.IssueProbeTicket(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if ticket != "" {
		t.Fatalf("未配置域名应回空串，得 %q", ticket)
	}
}

func TestProbeTicketExpiresPG(t *testing.T) {
	de, ctx, user, _ := setupProbe(t)
	uctx := userCtx(ctx, user)

	origNow := de.svc.now
	base := probeTestNow
	var cur time.Time
	cur = base
	de.svc.now = func() time.Time { return cur }
	t.Cleanup(func() { de.svc.now = origNow })

	ticket, err := de.svc.IssueProbeTicket(uctx)
	if err != nil || ticket == "" {
		t.Fatalf("签发: %v %q", err, ticket)
	}
	cur = base.Add(5*time.Minute + time.Second)
	_, err = de.svc.ProbeFrameDomains(uctx, ticket)
	assertUnauthorized(t, err)
}

func TestProbeTicketGroupChangedPG(t *testing.T) {
	de, ctx, user, _ := setupProbe(t)
	uctx := userCtx(ctx, user)
	admin := adminCtx(ctx, uuid.New())

	ticket, err := de.svc.IssueProbeTicket(uctx)
	if err != nil || ticket == "" {
		t.Fatalf("签发: %v %q", err, ticket)
	}

	_, err = de.svc.AdminSaveDomainConfig(admin, []DomainGroupInput{{
		Name: "缺省", IsDefault: true,
		Domains: []Domain{
			{BaseURL: "https://sub-a.example.test", Weight: 1},
			{BaseURL: "https://sub-b.example.test", Weight: 1},
			{BaseURL: "https://sub-c.example.test", Weight: 1},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)

	_, err = de.svc.ProbeFrameDomains(uctx, ticket)
	assertUnauthorized(t, err)

	ticket2, err := de.svc.IssueProbeTicket(uctx)
	if err != nil || ticket2 == "" {
		t.Fatalf("再签发: %v %q", err, ticket2)
	}
	doms, err := de.svc.ProbeFrameDomains(uctx, ticket2)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://sub-a.example.test",
		"https://sub-b.example.test",
		"https://sub-c.example.test",
	}
	if len(doms) != len(want) {
		t.Fatalf("域名 %v，要 %v", doms, want)
	}
	for i := range want {
		if doms[i] != want[i] {
			t.Fatalf("顺序或值不对：%v", doms)
		}
	}
}

func TestProbeReportOwnGroupOnlyPG(t *testing.T) {
	de, ctx, user, rdb := setupProbe(t)
	admin := adminCtx(ctx, uuid.New())
	G := seedUserGroup(t, de.env, ctx, "vip-probe")
	_, err := de.svc.AdminSaveDomainConfig(admin, []DomainGroupInput{
		{
			Name: "缺省", IsDefault: true,
			Domains: []Domain{
				{BaseURL: "https://sub-a.example.test", Weight: 1},
				{BaseURL: "https://sub-b.example.test", Weight: 1},
			},
		},
		{
			Name:         "专线",
			Domains:      []Domain{{BaseURL: "https://vip.example.test", Weight: 1}},
			UserGroupIDs: []uuid.UUID{G},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)

	// 先固定时钟再签发：票据的过期时间按签发时的时钟算
	origNow := de.svc.now
	base := probeTestNow
	de.svc.now = func() time.Time { return base }
	t.Cleanup(func() { de.svc.now = origNow })

	uctx := userCtx(ctx, user)
	ticket, err := de.svc.IssueProbeTicket(uctx)
	if err != nil || ticket == "" {
		t.Fatalf("签发: %v %q", err, ticket)
	}

	// 成功要同一小时 3 个不同账号：先放两个别的账号，本账号是第 3 个
	seedProbeReporters(t, rdb, "https://sub-a.example.test", base, "other-1", "other-2")
	err = de.svc.ReportProbeResults(uctx, ticket, []ProbeResult{
		{BaseURL: "https://vip.example.test", OK: true},
		{BaseURL: "https://SUB-A.example.test/", OK: true},
		{BaseURL: "https://sub-a.example.test", OK: false},
		{BaseURL: "not a url", OK: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	waitScore(t, de.svc.scores, "https://sub-a.example.test", base, 1.1)
	if rdb.HExists(ctx, domainScoreKey, "https://vip.example.test").Val() {
		t.Fatal("vip 不在用户组，不应写入分数 hash")
	}
}

func TestProbeReportTicketOncePG(t *testing.T) {
	de, ctx, user, rdb := setupProbe(t)
	uctx := userCtx(ctx, user)

	origNow := de.svc.now
	base := probeTestNow
	de.svc.now = func() time.Time { return base }
	t.Cleanup(func() { de.svc.now = origNow })

	ticket, err := de.svc.IssueProbeTicket(uctx)
	if err != nil || ticket == "" {
		t.Fatalf("签发: %v %q", err, ticket)
	}

	b := "https://sub-b.example.test"
	seedProbeReporters(t, rdb, b, base, "other-1", "other-2")
	if err := de.svc.ReportProbeResults(uctx, ticket, []ProbeResult{{BaseURL: b, OK: true}}); err != nil {
		t.Fatal(err)
	}
	waitScore(t, de.svc.scores, b, base, 1.1)

	seenKey := probeSeenKeyPrefix + user.String() + "|" + b
	if err := rdb.Del(ctx, seenKey).Err(); err != nil {
		t.Fatal(err)
	}
	if err := de.svc.ReportProbeResults(uctx, ticket, []ProbeResult{{BaseURL: b, OK: true}}); err != nil {
		t.Fatal(err)
	}
	scores, ok := de.svc.scores.Read(ctx, []string{b}, base)
	if !ok || !almost(scores[b], 1.1, 1e-3) {
		t.Fatalf("同票据第二次上报不应再加分：%v（ok=%v）", scores[b], ok)
	}
}

func TestProbeReportBadTicketPG(t *testing.T) {
	de, ctx, user, rdb := setupProbe(t)
	uctx := userCtx(ctx, user)

	err := de.svc.ReportProbeResults(uctx, "x.y", []ProbeResult{
		{BaseURL: "https://sub-a.example.test", OK: true},
	})
	assertUnauthorized(t, err)
	if rdb.Exists(ctx, domainScoreKey).Val() != 0 {
		t.Fatal("坏票据不应创建分数 hash")
	}
}

// 成功与失败对称：同一小时只有 2 个账号报某域名成功不加分，第 3 个起才加分。
func TestProbeReportSuccessNeedsThreeAccountsPG(t *testing.T) {
	de, ctx, user, rdb := setupProbe(t)
	origNow := de.svc.now
	base := probeTestNow
	de.svc.now = func() time.Time { return base }
	t.Cleanup(func() { de.svc.now = origNow })

	uctx := userCtx(ctx, user)
	ticket, err := de.svc.IssueProbeTicket(uctx)
	if err != nil || ticket == "" {
		t.Fatalf("签发: %v %q", err, ticket)
	}
	a, b := "https://sub-a.example.test", "https://sub-b.example.test"
	seedProbeReporters(t, rdb, a, base, "other-1")            // 本账号是第 2 个
	seedProbeReporters(t, rdb, b, base, "other-1", "other-2") // 本账号是第 3 个
	if err := de.svc.ReportProbeResults(uctx, ticket, []ProbeResult{{BaseURL: a, OK: true}, {BaseURL: b, OK: true}}); err != nil {
		t.Fatal(err)
	}
	if rdb.HExists(ctx, domainScoreKey, a).Val() {
		t.Fatal("只有 2 个账号报成功，a 不该加分")
	}
	waitScore(t, de.svc.scores, b, base, 1.1)
}
