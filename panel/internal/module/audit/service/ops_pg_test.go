package service

import (
	"context"
	"encoding/hex"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

func userCtx(ctx context.Context, user uuid.UUID, ip string) context.Context {
	ctx = reqid.With(ctx, reqid.New())
	ctx = authn.With(ctx, authn.Principal{
		UserID:    user,
		SessionID: "s1",
		Audience:  authn.AudiencePublic,
	})
	return reqmeta.With(ctx, reqmeta.Meta{IP: ip, UserAgent: "ua-test/1"})
}

func adminCtx(ctx context.Context, admin uuid.UUID) context.Context {
	ctx = reqid.With(ctx, reqid.New())
	ctx = authn.With(ctx, authn.Principal{
		UserID:    admin,
		SessionID: "s1",
		Audience:  authn.AudienceAdmin,
	})
	return reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.250", UserAgent: "ua-test/1"})
}

func mustCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	e := errs.As(err)
	if e == nil || e.Code != code {
		t.Fatalf("期望错误码 %s，得到 %v", code, err)
	}
}

func findClusterByIP(clusters []Cluster, ip string) *Cluster {
	for i := range clusters {
		if clusters[i].IP == ip {
			return &clusters[i]
		}
	}
	return nil
}

func uuidSetEqual(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]uuid.UUID(nil), a...)
	sb := append([]uuid.UUID(nil), b...)
	sort.Slice(sa, func(i, j int) bool { return sa[i].String() < sa[j].String() })
	sort.Slice(sb, func(i, j int) bool { return sb[i].String() < sb[j].String() })
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func TestCSVSafe(t *testing.T) {
	if CSVSafe("=1+1") != "'=1+1" {
		t.Fatalf("公式前缀")
	}
	if CSVSafe("a@b") != "a@b" {
		t.Fatal("普通文本不变")
	}
	if CSVSafe("") != "" {
		t.Fatal("空串")
	}
}

func TestUserActivityPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	ipA := "203.0.113.10"
	ipB := "203.0.113.11"
	U := uuid.New()
	V := uuid.New()

	if err := svc.Write(userCtx(ctx, U, ipA), Event{Action: "user.registered"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := svc.Write(userCtx(ctx, U, ipA), Event{Action: "user.login"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Write(userCtx(ctx, U, ipB), Event{Action: "user.login"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Write(userCtx(ctx, V, ipA), Event{Action: "user.login"}); err != nil {
		t.Fatal(err)
	}

	act, err := svc.UserActivity(ctx, U)
	if err != nil {
		t.Fatal(err)
	}
	if act.RegisteredIP != ipA {
		t.Fatalf("RegisteredIP %q want %q", act.RegisteredIP, ipA)
	}
	if act.Events == nil || len(act.Events) != 4 {
		t.Fatalf("Events len %d want 4", len(act.Events))
	}
	for i := 1; i < len(act.Events); i++ {
		if act.Events[i].At.After(act.Events[i-1].At) {
			t.Fatal("Events 应按时间倒序")
		}
	}
	for _, e := range act.Events {
		if e.Audience != authn.AudiencePublic {
			t.Fatalf("Audience %q", e.Audience)
		}
		if e.IP != ipA && e.IP != ipB {
			t.Fatalf("IP %q", e.IP)
		}
	}
	if len(act.IPs) < 2 {
		t.Fatalf("IPs len %d", len(act.IPs))
	}
	if act.IPs[0].IP != ipA || act.IPs[0].Count != 3 || act.IPs[0].Accounts != 2 {
		t.Fatalf("IPs[0] %+v", act.IPs[0])
	}
	if !uuidSetEqual(act.IPs[0].AccountIDs, []uuid.UUID{U, V}) {
		t.Fatalf("IPs[0] AccountIDs %v", act.IPs[0].AccountIDs)
	}
	var ipBRow *ActivityIP
	for i := range act.IPs {
		if act.IPs[i].IP == ipB {
			ipBRow = &act.IPs[i]
			break
		}
	}
	if ipBRow == nil || ipBRow.Accounts != 1 {
		t.Fatal("IP B 统计")
	}
	if ipBRow.AccountIDs == nil {
		t.Fatal("B AccountIDs 应为空切片而非 nil")
	}
	if len(ipBRow.AccountIDs) != 0 {
		t.Fatalf("B AccountIDs len %d", len(ipBRow.AccountIDs))
	}

	empty := uuid.New()
	act0, err := svc.UserActivity(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}
	if act0.RegisteredIP != "" {
		t.Fatal("无审计用户 RegisteredIP 应为空")
	}
	if act0.Events == nil || len(act0.Events) != 0 {
		t.Fatal("Events 应为非 nil 空切片")
	}
	if act0.IPs == nil || len(act0.IPs) != 0 {
		t.Fatal("IPs 应为非 nil 空切片")
	}
}

func TestAccessEventsPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	p := uniqueAction(t)
	actor1 := uuid.New()
	actor2 := uuid.New()
	ip1 := "203.0.113.20"
	ip2 := "203.0.113.21"

	writes := []struct {
		user    uuid.UUID
		ip      string
		action  string
		outcome string
	}{
		{actor1, ip1, p + ".login", OutcomeSuccess},
		{actor1, ip2, p + ".order", OutcomeSuccess},
		{actor2, ip1, p + ".login", OutcomeFailure},
		{actor1, ip1, p + ".other", OutcomeDenied},
	}
	for _, w := range writes {
		e := Event{Action: w.action, Outcome: w.outcome}
		if err := svc.Write(userCtx(ctx, w.user, w.ip), e); err != nil {
			t.Fatal(err)
		}
	}

	all, err := svc.AccessEvents(ctx, AccessQuery{Include: []string{p}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("Include 批次 len %d want 4", len(all))
	}

	noOrder, err := svc.AccessEvents(ctx, AccessQuery{
		Include: []string{p},
		Exclude: []string{p + ".order"},
		Limit:   50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range noOrder {
		if strings.HasPrefix(e.Action, p+".order") {
			t.Fatalf("Exclude 仍含 order: %s", e.Action)
		}
	}
	if len(noOrder) != 3 {
		t.Fatalf("Exclude order len %d", len(noOrder))
	}

	byIP, err := svc.AccessEvents(ctx, AccessQuery{Include: []string{p}, IP: ip2, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(byIP) != 1 || byIP[0].IP != ip2 {
		t.Fatalf("IP 精确匹配 %+v", byIP)
	}

	emptyActors, err := svc.AccessEvents(ctx, AccessQuery{
		Include: []string{p}, FilterActors: true, ActorIDs: nil, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if emptyActors == nil {
		t.Fatal("FilterActors 空应非 nil 切片")
	}
	if len(emptyActors) != 0 {
		t.Fatalf("FilterActors 空 len %d", len(emptyActors))
	}

	oneActor, err := svc.AccessEvents(ctx, AccessQuery{
		Include: []string{p}, FilterActors: true, ActorIDs: []uuid.UUID{actor2}, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(oneActor) != 1 || oneActor[0].ActorID == nil || *oneActor[0].ActorID != actor2 {
		t.Fatalf("单操作者 %+v", oneActor)
	}

	errOnly, err := svc.AccessEvents(ctx, AccessQuery{Include: []string{p}, Outcome: "error", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range errOnly {
		if e.Outcome == OutcomeSuccess {
			t.Fatalf("error 筛选含 success: %s", e.Action)
		}
	}
	if len(errOnly) != 2 {
		t.Fatalf("error outcome len %d want 2", len(errOnly))
	}

	lim1, err := svc.AccessEvents(ctx, AccessQuery{Include: []string{p}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(lim1) != 1 {
		t.Fatalf("Limit 1 len %d", len(lim1))
	}
	if lim1[0].Action != writes[len(writes)-1].action {
		t.Fatalf("Limit 1 应是最新写入 %q got %q", writes[len(writes)-1].action, lim1[0].Action)
	}
}

func TestAccessEventsTargetActionsPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	victim := uuid.New()
	other := uuid.New()
	p := uniqueAction(t)

	if err := svc.Write(userCtx(ctx, victim, "203.0.113.30"), Event{Action: p + ".login"}); err != nil {
		t.Fatal(err)
	}
	anon := func(ip string) context.Context {
		return reqmeta.With(reqid.With(ctx, reqid.New()), reqmeta.Meta{IP: ip, UserAgent: "ua-test/1"})
	}
	if err := svc.Write(anon("203.0.113.31"), Event{
		Action: p + ".login_failed", Outcome: OutcomeFailure, ResourceType: "user", ResourceID: &victim,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Write(anon("203.0.113.32"), Event{
		Action: p + ".refresh", Outcome: OutcomeDenied, ResourceType: "user", ResourceID: &victim,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Write(anon("203.0.113.33"), Event{
		Action: p + ".login_failed", Outcome: OutcomeFailure, ResourceType: "user", ResourceID: &other,
	}); err != nil {
		t.Fatal(err)
	}

	actorOnly, err := svc.AccessEvents(ctx, AccessQuery{
		Include: []string{p}, FilterActors: true, ActorIDs: []uuid.UUID{victim}, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(actorOnly) != 1 || actorOnly[0].Action != p+".login" {
		t.Fatalf("FilterActors only len=%d %+v", len(actorOnly), actorOnly)
	}

	withTarget, err := svc.AccessEvents(ctx, AccessQuery{
		Include: []string{p}, FilterActors: true, ActorIDs: []uuid.UUID{victim},
		TargetActions: []string{p + ".login_failed"}, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTwo := map[string]bool{p + ".login": true, p + ".login_failed": true}
	if len(withTarget) != 2 {
		t.Fatalf("with TargetActions len %d want 2", len(withTarget))
	}
	for _, e := range withTarget {
		if !wantTwo[e.Action] {
			t.Fatalf("unexpected action %q", e.Action)
		}
	}
	var failed *AccessEvent
	for i := range withTarget {
		if withTarget[i].Action == p+".login_failed" {
			failed = &withTarget[i]
			break
		}
	}
	if failed == nil {
		t.Fatal("missing login_failed")
	}
	if failed.ActorID != nil {
		t.Fatalf("login_failed ActorID %v want nil", failed.ActorID)
	}
	if failed.ResourceType != "user" || failed.ResourceID == nil || *failed.ResourceID != victim {
		t.Fatalf("login_failed resource %+v", failed)
	}
	if failed.IP != "203.0.113.31" {
		t.Fatalf("login_failed IP %q", failed.IP)
	}

	all, err := svc.AccessEvents(ctx, AccessQuery{Include: []string{p}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("no FilterActors len %d want 4", len(all))
	}
	var loginEv *AccessEvent
	for i := range all {
		if all[i].Action == p+".login" {
			loginEv = &all[i]
			break
		}
	}
	if loginEv == nil {
		t.Fatal("missing login in all")
	}
	if loginEv.ResourceType != "" || loginEv.ResourceID != nil {
		t.Fatalf("login resource type=%q id=%v want empty/nil", loginEv.ResourceType, loginEv.ResourceID)
	}
}

func TestIPClustersPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	keys := testKeys(t)

	ipC := "203.0.113.30"
	ipD := "203.0.113.31"
	U1, U2, U3 := uuid.New(), uuid.New(), uuid.New()
	admin := uuid.New()
	actx := adminCtx(ctx, admin)

	for _, u := range []uuid.UUID{U1, U2} {
		if err := svc.Write(userCtx(ctx, u, ipC), Event{Action: "user.login"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Write(userCtx(ctx, U3, ipD), Event{Action: "user.login"}); err != nil {
		t.Fatal(err)
	}

	list, err := svc.IPClusters(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	cCluster := findClusterByIP(list, ipC)
	if cCluster == nil {
		t.Fatal("应能找到 IP C 聚类")
	}
	if cCluster.Accounts != 2 || cCluster.Events != 2 {
		t.Fatalf("C 统计 %+v", *cCluster)
	}
	if len(cCluster.Key) != 64 {
		t.Fatalf("Key len %d", len(cCluster.Key))
	}
	if _, err := hex.DecodeString(cCluster.Key); err != nil {
		t.Fatal(err)
	}
	if !uuidSetEqual(cCluster.AccountIDs, []uuid.UUID{U1, U2}) {
		t.Fatalf("AccountIDs %v", cCluster.AccountIDs)
	}
	if cCluster.Review != nil {
		t.Fatal("初始 Review 应为 nil")
	}
	if findClusterByIP(list, ipD) != nil {
		t.Fatal("IP D 不应出现在聚类列表")
	}

	members, err := svc.ClusterMembers(ctx, cCluster.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !uuidSetEqual(members, []uuid.UUID{U1, U2}) {
		t.Fatalf("ClusterMembers %v", members)
	}

	bogusKey := strings.Repeat("a", 64)
	_, err = svc.ClusterMembers(ctx, bogusKey)
	mustCode(t, err, errs.NotFound)

	_, err = svc.ClusterMembers(ctx, "zz")
	mustCode(t, err, errs.NotFound)

	dHash := keys.HMAC(crypto.InfoHMACIP, []byte(ipD))
	dKey := hex.EncodeToString(dHash)
	_, err = svc.ClusterMembers(ctx, dKey)
	mustCode(t, err, errs.NotFound)

	before := time.Now()
	expires, err := svc.MarkClusterNormal(actx, cCluster.Key, "家庭宽带")
	if err != nil {
		t.Fatal(err)
	}
	wantMin := before.Add(ClusterNormalTTL).Add(-time.Minute)
	wantMax := before.Add(ClusterNormalTTL).Add(time.Minute)
	if expires.Before(wantMin) || expires.After(wantMax) {
		t.Fatalf("expires %v 不在 ±1 分钟", expires)
	}

	listHide, err := svc.IPClusters(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if findClusterByIP(listHide, ipC) != nil {
		t.Fatal("标记 normal 后默认列表不应含 C")
	}

	listRev, err := svc.IPClusters(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	cRev := findClusterByIP(listRev, ipC)
	if cRev == nil || cRev.Review == nil {
		t.Fatal("含复核列表应有 C")
	}
	if cRev.Review.Decision != ClusterNormal || cRev.Review.ExpiresAt == nil {
		t.Fatalf("Review %+v", cRev.Review)
	}

	if scanCount(t, d.Conn(ctx).Raw(countSQL, "risk.ip_cluster.mark_normal").Row()) != 1 {
		t.Fatal("mark_normal 审计计数")
	}

	longNote := strings.Repeat("备", 501)
	_, err = svc.MarkClusterNormal(actx, cCluster.Key, longNote)
	mustCode(t, err, errs.ValidationFailed)

	_, err = svc.MarkClusterNormal(ctx, cCluster.Key, "无主体")
	mustCode(t, err, errs.Unauthorized)

	disID, err := svc.MarkClusterDisabled(actx, cCluster.Key, "批量注册")
	if err != nil || disID == uuid.Nil {
		t.Fatalf("MarkClusterDisabled %v id %v", err, disID)
	}

	listBack, err := svc.IPClusters(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	cBack := findClusterByIP(listBack, ipC)
	if cBack == nil || cBack.Review == nil {
		t.Fatal("disabled 后默认列表应再含 C")
	}
	if cBack.Review.Decision != ClusterDisabled || cBack.Review.ExpiresAt != nil {
		t.Fatalf("disabled Review %+v", cBack.Review)
	}
}

const sourceIPViewSQL = `
	SELECT resource_id, data->>'view' AS view
	  FROM audit_events
	 WHERE action = 'security.source_ip_viewed'
	 ORDER BY occurred_at, id`

type sourceIPViewRow struct {
	ResourceID *uuid.UUID
	View       string
}

func TestRecordSourceIPViewPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	admin := uuid.New()
	actx := adminCtx(ctx, admin)
	u := uuid.New()

	if err := svc.RecordSourceIPView(actx, ViewTrace, &u, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordSourceIPView(actx, ViewTrace, nil, map[string]any{"masked": "203.0.*.*"}); err != nil {
		t.Fatal(err)
	}

	rows, err := d.Conn(ctx).Raw(sourceIPViewSQL).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []sourceIPViewRow
	for rows.Next() {
		var r sourceIPViewRow
		if err := rows.Scan(&r.ResourceID, &r.View); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("行数 %d want 2", len(got))
	}
	if got[0].View != ViewTrace {
		t.Fatalf("第一行 view %q", got[0].View)
	}
	if got[0].ResourceID == nil || *got[0].ResourceID != u {
		t.Fatalf("第一行 resource_id %v", got[0].ResourceID)
	}
	if got[1].View != ViewTrace {
		t.Fatalf("第二行 view %q", got[1].View)
	}
	if got[1].ResourceID != nil {
		t.Fatalf("第二行 resource_id 应为 NULL，得 %v", got[1].ResourceID)
	}
}
