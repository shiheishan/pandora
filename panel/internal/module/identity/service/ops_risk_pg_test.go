package service

import (
	"context"
	"strings"
	"testing"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
)

func riskUsers(t *testing.T, e *env, ctx context.Context, tag, ip string) (
	u1, u2, u3, staff uuid.UUID, admin uuid.UUID, actx context.Context,
) {
	t.Helper()
	admin = uuid.New()
	actx = asAdmin(ctx, admin)
	u1 = e.mkUser(t, ctx, uniqEmail(t, tag+"-1"), "Passw0rdLong1").ID
	u2 = e.mkUser(t, ctx, uniqEmail(t, tag+"-2"), "Passw0rdLong1").ID
	u3 = e.mkUser(t, ctx, uniqEmail(t, tag+"-3"), "Passw0rdLong1").ID
	staff = e.mkUser(t, ctx, uniqEmail(t, tag+"-s"), "Passw0rdLong1").ID
	e.staff.staff[staff] = true
	for _, u := range []uuid.UUID{u1, u2, u3, staff} {
		writeEvent(t, e, ctx, u, ip, "user.login")
	}
	writeEvent(t, e, ctx, u1, "203.0.113.61", "user.login")
	return u1, u2, u3, staff, admin, actx
}

func clusterByIP(clusters []IPCluster, ip string) (IPCluster, bool) {
	for _, c := range clusters {
		if c.IP == ip {
			return c, true
		}
	}
	return IPCluster{}, false
}

func TestPGUserProfile(t *testing.T) {
	e, ctx, fs := opsEnv(t)
	ip := "203.0.113.60"
	u1, _, _, _, admin, actx := riskUsers(t, e, ctx, "prof", ip)
	fs.userFetches = []FetchRecord{{Result: "ok", At: time.Now()}}
	fs.sources = 3

	prof, err := e.svc.UserProfile(asAdmin(ctx, admin), u1)
	if err != nil {
		t.Fatal(err)
	}
	logins := 0
	for _, ev := range prof.Events {
		if ev.Action == "user.login" && ev.IP != "" {
			logins++
			if ev.Domain != "public" {
				t.Fatalf("domain %q", ev.Domain)
			}
		}
	}
	if logins != 2 {
		t.Fatalf("login events %d", logins)
	}
	// 两个来源各 1 次、同一事务里时间相同，排序落到哈希上，按 IP 找而不是按下标
	var shared *ProfileIP
	for i := range prof.IPs {
		if prof.IPs[i].IP == ip {
			shared = &prof.IPs[i]
		}
	}
	if len(prof.IPs) != 2 || shared == nil || shared.Accounts != 4 {
		t.Fatalf("ips %+v", prof.IPs)
	}
	if len(prof.Related) != 3 {
		t.Fatalf("related %d", len(prof.Related))
	}
	for i := 1; i < len(prof.Related); i++ {
		if strings.Compare(prof.Related[i-1].Email, prof.Related[i].Email) > 0 {
			t.Fatalf("related order %+v", prof.Related)
		}
	}
	if len(prof.Fetches) != 1 || prof.FetchSources7d != fs.sources {
		t.Fatalf("fetches %+v sources=%d", prof.Fetches, prof.FetchSources7d)
	}
	// 用户 10-21 定：读画像不记「看了 IP」审计（只有溯源「显示」明文那一处记）
	if e.auditCount(ctx, "security.source_ip_viewed", u1) != 0 {
		t.Fatal("读画像不该记 source_ip_viewed")
	}
	if prof.Risk != "mid" {
		t.Fatalf("prof.Risk %q", prof.Risk)
	}
	if shared.Risk != "mid" || shared.Geo != "" || shared.NetworkKind != "" {
		t.Fatalf("shared ip %+v", *shared)
	}
	for _, pip := range prof.IPs {
		if pip.IP == "203.0.113.61" && pip.Risk != "" {
			t.Fatalf("203.0.113.61 risk %q", pip.Risk)
		}
	}
	_, err = e.svc.UserProfile(actx, uuid.New())
	if codeOf(err) != errs.NotFound {
		t.Fatalf("random user: %v", err)
	}
}

func TestPGAccessLog(t *testing.T) {
	e, ctx, fs := opsEnv(t)
	ip := "203.0.113.62"
	u1, u2, _, _, _, actx := riskUsers(t, e, ctx, "alog", ip)
	u1u, _ := e.svc.Get(ctx, u1)

	items, err := e.svc.AccessLog(actx, AccessLogQuery{Category: "login", User: u1.String()})
	if err != nil || len(items) != 2 {
		t.Fatalf("login len=%d err=%v", len(items), err)
	}
	for _, it := range items {
		if it.UserEmail != u1u.Email || it.Category != "login" {
			t.Fatalf("item %+v", it)
		}
		if it.Geo != "" || it.NetworkKind != "" {
			t.Fatalf("geo/kind %+v", it)
		}
	}

	now := time.Now().UTC()
	fs.fetches = []FetchRecord{{UserID: &u1, Result: "ok", At: now.Add(-time.Hour)}}
	items, err = e.svc.AccessLog(actx, AccessLogQuery{User: u1.String()})
	if err != nil {
		t.Fatal(err)
	}
	// 2 login + 1 fetch from fake (audit may still contribute logins)
	if len(items) < 3 {
		t.Fatalf("merged len=%d", len(items))
	}
	if !fs.lastFetch.FilterUsers {
		t.Fatalf("lastFetch %+v", fs.lastFetch)
	}
	hasSub := false
	for _, it := range items {
		if it.Category == "subscribe" && it.Action == "subscription.fetch" {
			hasSub = true
		}
	}
	if !hasSub {
		t.Fatalf("no subscribe item %+v", items)
	}
	if items[0].At.Before(items[len(items)-1].At) {
		t.Fatalf("not desc: %v .. %v", items[0].At, items[len(items)-1].At)
	}

	// 假端口不按条件筛，后面按 IP、按邮箱片段的断言只看审计这一路
	fs.fetches = nil
	callsBefore := fs.fetchCalls
	items, err = e.svc.AccessLog(actx, AccessLogQuery{Category: "subscribe", Outcome: "failure"})
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("subscribe failure len=%d err=%v", len(items), err)
	}
	if fs.fetchCalls != callsBefore {
		t.Fatalf("subscribe+failure 不应调 Fetches: %d -> %d", callsBefore, fs.fetchCalls)
	}

	_, err = e.svc.AccessLog(actx, AccessLogQuery{Category: "x"})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["category"] == "" {
		t.Fatalf("category: %v", err)
	}
	_, err = e.svc.AccessLog(actx, AccessLogQuery{Outcome: "x"})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["outcome"] == "" {
		t.Fatalf("outcome: %v", err)
	}
	_, err = e.svc.AccessLog(actx, AccessLogQuery{Offset: 9801})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["offset"] == "" {
		t.Fatalf("offset: %v", err)
	}

	items, err = e.svc.AccessLog(actx, AccessLogQuery{IP: "203.0.113.61"})
	if err != nil || len(items) != 1 {
		t.Fatalf("ip61 len=%d err=%v", len(items), err)
	}

	items, err = e.svc.AccessLog(actx, AccessLogQuery{User: "不存在的片段zz"})
	if err != nil || len(items) != 0 {
		t.Fatalf("bad user fragment len=%d err=%v", len(items), err)
	}

	op := e.svc.ops.(*auditsvc.Service)
	anonCtx := reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.64", UserAgent: "python-requests/2.31"})
	if err := op.Write(anonCtx, auditsvc.Event{
		Action: "user.login_failed", Outcome: auditsvc.OutcomeFailure,
		ResourceType: "user", ResourceID: &u1,
		Data: map[string]any{"reason": "invalid_credentials"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := op.Write(anonCtx, auditsvc.Event{
		Action: "user.login_failed", Outcome: auditsvc.OutcomeFailure,
		ResourceType: "user", ResourceID: &u2,
		Data: map[string]any{"reason": "invalid_credentials"},
	}); err != nil {
		t.Fatal(err)
	}

	items, err = e.svc.AccessLog(actx, AccessLogQuery{Category: "login", User: u1.String()})
	if err != nil || len(items) != 3 {
		t.Fatalf("login+u1 len=%d err=%v", len(items), err)
	}
	var failU1 *AccessItem
	for i := range items {
		if items[i].Action == "user.login_failed" {
			failU1 = &items[i]
			break
		}
	}
	if failU1 == nil || failU1.UserID != nil || failU1.TargetUserID == nil || *failU1.TargetUserID != u1 {
		t.Fatalf("fail u1 %+v", failU1)
	}
	if failU1.TargetUserEmail != u1u.Email || failU1.Outcome != "failure" || failU1.IP != "203.0.113.64" || failU1.Category != "login" {
		t.Fatalf("fail u1 fields %+v", *failU1)
	}

	items, err = e.svc.AccessLog(actx, AccessLogQuery{Category: "login", User: u1u.Email})
	if err != nil {
		t.Fatal(err)
	}
	foundEmail := false
	for _, it := range items {
		if it.TargetUserID != nil && *it.TargetUserID == u1 {
			foundEmail = true
		}
	}
	if !foundEmail {
		t.Fatalf("email filter missing fail for u1 %+v", items)
	}

	items, err = e.svc.AccessLog(actx, AccessLogQuery{IP: "203.0.113.64"})
	if err != nil || len(items) != 2 {
		t.Fatalf("ip64 len=%d err=%v", len(items), err)
	}
	byTarget := map[uuid.UUID]AccessItem{}
	for _, it := range items {
		if it.TargetUserID != nil {
			byTarget[*it.TargetUserID] = it
		}
	}
	if len(byTarget) != 2 || byTarget[u1].TargetUserEmail == "" || byTarget[u2].TargetUserEmail == "" {
		t.Fatalf("ip64 targets %+v", byTarget)
	}
}

func TestPGIPClustersAndReview(t *testing.T) {
	e, ctx, fs := opsEnv(t)
	ip := "203.0.113.63"
	u1, u2, _, _, admin, actx := riskUsers(t, e, ctx, "clus", ip)
	u1u, _ := e.svc.Get(ctx, u1)
	u2u, _ := e.svc.Get(ctx, u2)

	planLive := "季付"
	planOld := "旧套餐"
	fs.current[u1] = CurrentSub{PlanName: planLive, Live: true}
	fs.current[u2] = CurrentSub{PlanName: planOld, Live: false}

	clusters, err := e.svc.IPClusters(asAdmin(ctx, admin), false)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := clusterByIP(clusters, ip)
	if !ok {
		t.Fatalf("no cluster for %s in %+v", ip, clusters)
	}
	if c.Accounts != 4 || c.Risk != "mid" || len(c.Key) != 64 {
		t.Fatalf("cluster %+v", c)
	}
	if len(c.Users) != 4 {
		t.Fatalf("users %d", len(c.Users))
	}
	for i := 1; i < len(c.Users); i++ {
		if strings.Compare(c.Users[i-1].Email, c.Users[i].Email) > 0 {
			t.Fatalf("user order %+v", c.Users)
		}
	}
	if len(c.Emails) != len(c.Users) || c.Emails[0] != c.Users[0].Email {
		t.Fatalf("emails %+v", c.Emails)
	}
	var u1c, u2c *ClusterUser
	for i := range c.Users {
		if c.Users[i].Email == u1u.Email {
			u1c = &c.Users[i]
		}
		if c.Users[i].Email == u2u.Email {
			u2c = &c.Users[i]
		}
	}
	if u1c == nil || u1c.ActivePlan == nil || *u1c.ActivePlan != planLive {
		t.Fatalf("u1 plan %+v", u1c)
	}
	if u2c == nil || u2c.ActivePlan != nil {
		t.Fatalf("u2 plan %+v", u2c)
	}
	if c.Review != nil {
		t.Fatalf("review should be nil %+v", c.Review)
	}

	exp, err := e.svc.ReviewIPCluster(actx, c.Key, "同一办公室")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(30 * 24 * time.Hour)
	if exp.Before(want.Add(-2*time.Minute)) || exp.After(want.Add(2*time.Minute)) {
		t.Fatalf("expires %v want ~%v", exp, want)
	}
	clusters, err = e.svc.IPClusters(actx, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := clusterByIP(clusters, ip); ok {
		t.Fatal("reviewed cluster should hide from default list")
	}
	clusters, err = e.svc.IPClusters(actx, true)
	if err != nil {
		t.Fatal(err)
	}
	c2, ok := clusterByIP(clusters, ip)
	if !ok || c2.Review == nil || c2.Review.Decision != "normal" {
		t.Fatalf("include reviewed %+v ok=%v", c2.Review, ok)
	}
}

func TestPGDisableIPClusterAccounts(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	ip := "203.0.113.64"
	u1, u2, u3, staff, admin, actx := riskUsers(t, e, ctx, "dis", ip)
	if err := e.exec(ctx, `UPDATE users SET status = ? WHERE id = ?`, StatusSuspended, u2); err != nil {
		t.Fatal(err)
	}
	clusters, err := e.svc.IPClusters(actx, false)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := clusterByIP(clusters, ip)
	if !ok {
		t.Fatal("cluster missing")
	}
	key := c.Key
	randID := uuid.New()
	ids := []uuid.UUID{u1, u2, u3, staff, admin, randID, u1}

	res, err := e.svc.DisableIPClusterAccounts(actx, key, ids, "批量注册嫌疑账号")
	if err != nil || res.Disabled != 2 {
		t.Fatalf("disabled=%d err=%v skips=%+v", res.Disabled, err, res.Skipped)
	}
	wantSkip := map[uuid.UUID]string{
		u2: SkipAlreadyDisabled, staff: SkipAdministrator, admin: SkipSelf, randID: SkipNotMember,
	}
	for uid, reason := range wantSkip {
		found := false
		for _, sk := range res.Skipped {
			if sk.UserID == uid && sk.Reason == reason {
				found = true
			}
		}
		if !found {
			t.Fatalf("skip %s %s in %+v", uid, reason, res.Skipped)
		}
	}
	u1s, _ := e.svc.Get(ctx, u1)
	u3s, _ := e.svc.Get(ctx, u3)
	if u1s.Status != StatusSuspended || u3s.Status != StatusSuspended {
		t.Fatalf("status u1=%s u3=%s", u1s.Status, u3s.Status)
	}
	revoked := map[uuid.UUID]bool{}
	for _, c := range e.revoker.calls {
		revoked[c.user] = true
	}
	if !revoked[u1] || !revoked[u3] {
		t.Fatalf("revoke %+v", e.revoker.calls)
	}
	if e.auditCount(ctx, ActionStatusChange, u1) != 1 {
		t.Fatal("status audit u1")
	}
	var outcome string
	if err := e.row(ctx, `SELECT outcome FROM audit_events WHERE action = 'risk.ip_cluster.disable' ORDER BY occurred_at DESC, id DESC LIMIT 1`).Scan(&outcome); err != nil || outcome != "partial" {
		t.Fatalf("disable outcome %q err=%v", outcome, err)
	}
	clusters, _ = e.svc.IPClusters(actx, false)
	c3, _ := clusterByIP(clusters, ip)
	if c3.Review == nil || c3.Review.Decision != "disabled" {
		t.Fatalf("review after disable %+v", c3.Review)
	}

	res2, err := e.svc.DisableIPClusterAccounts(actx, key, ids, "批量注册嫌疑账号")
	if err != nil || res2.Disabled != 0 {
		t.Fatalf("second disabled=%d err=%v", res2.Disabled, err)
	}
	already := 0
	for _, sk := range res2.Skipped {
		if sk.Reason == SkipAlreadyDisabled && (sk.UserID == u1 || sk.UserID == u3) {
			already++
		}
	}
	if already < 2 {
		t.Fatalf("idempotent skips %+v", res2.Skipped)
	}
	if err := e.row(ctx, `SELECT outcome FROM audit_events WHERE action = 'risk.ip_cluster.disable' ORDER BY occurred_at DESC, id DESC LIMIT 1`).Scan(&outcome); err != nil || outcome != "failure" {
		t.Fatalf("second outcome %q err=%v", outcome, err)
	}

	_, err = e.svc.DisableIPClusterAccounts(actx, key, nil, "短")
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("empty+short: %v", err)
	}
	f := fieldsOf(err)
	if f["reason"] == "" || f["user_ids"] == "" {
		t.Fatalf("want reason and user_ids fields, got %v", f)
	}
	many := make([]uuid.UUID, 201)
	for i := range many {
		many[i] = uuid.New()
	}
	_, err = e.svc.DisableIPClusterAccounts(actx, key, many, "批量注册嫌疑账号")
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["user_ids"] == "" {
		t.Fatalf("201 ids: %v", err)
	}
	_, err = e.svc.DisableIPClusterAccounts(actx, "zz", []uuid.UUID{u1}, "批量注册嫌疑账号")
	if codeOf(err) != errs.NotFound {
		t.Fatalf("bad key: %v", err)
	}
}
