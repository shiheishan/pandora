package adminops

import (
	"reflect"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// 原用户画像「按 IP 归并」SQL（f364a62 的 user_profile.go 原文），只作 PG18 对照：
// 它 LEFT JOIN 视图 audit_ip_clusters，规划器要先把全租户 90 天的审计按 IP 整个聚合。
const legacyUserActivityIPsSQL = `
			SELECT COALESCE((array_agg(a.source_ip_enc ORDER BY a.occurred_at DESC))[1], ''::bytea),
			       count(*), min(a.occurred_at), max(a.occurred_at),
			       COALESCE(c.account_count, 1),
			       COALESCE(c.accounts, ARRAY[]::text[])
			  FROM audit_events a
			  LEFT JOIN audit_ip_clusters c
			    ON c.tenant_id = a.tenant_id AND c.source_ip_hash = a.source_ip_hash
			 WHERE a.tenant_id = $1 AND a.actor_id = $2::uuid
			   AND a.source_ip_hash IS NOT NULL
			 GROUP BY a.source_ip_hash, c.account_count, c.accounts
			 ORDER BY count(*) DESC LIMIT 20`

// TestUserActivityIPClustersPG18 钉住用户画像改成「先取该用户的来源、再逐个数同源账号」后
// 口径不变：每个来源的次数、首末时间、同源账号数与账号列表与原 SQL（经视图）相同；
// 同源只认用户事件（别的 actor_kind 不算），只有一个账号的来源不算同源。
func TestUserActivityIPClustersPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant = "7f000000-0000-4000-8000-000000000001"
		alice  = "7f000000-0000-4000-8000-000000000011"
		bob    = "7f000000-0000-4000-8000-000000000012"
		carol  = "7f000000-0000-4000-8000-000000000013"
	)
	if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'profile-ip-pg18','Profile IP','CNY')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		($2,$1,'alice@profile.invalid','Alice','active'),($3,$1,'bob@profile.invalid','Bob','active'),
		($4,$1,'carol@profile.invalid','Carol','active')`, tenant, alice, bob, carol); err != nil {
		t.Fatal(err)
	}
	h1, h2, h3 := []byte("profile-ip-hash-1"), []byte("profile-ip-hash-2"), []byte("profile-ip-hash-3")
	type ev struct {
		kind, actor string
		hash        []byte
	}
	// alice：h1 四次（其中一次是 admin 身份）、h3 两次、h2 一次，另有一条没有来源的；
	// bob：h1、h3 各一次；carol：h1 一次、h2 一次但以 system 身份（不算同源）。
	// 同一事务里写，occurred_at 都相同，所以各来源的次数取得互不相同，便于对齐比较
	events := []ev{
		{"user", alice, h1}, {"user", alice, h1}, {"user", alice, h1}, {"admin", alice, h1},
		{"user", alice, h3}, {"user", alice, h3},
		{"user", alice, h2}, {"user", alice, nil},
		{"user", bob, h1}, {"user", bob, h3},
		{"user", carol, h1}, {"system", carol, h2},
	}
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		for _, e := range events {
			actor := e.actor
			if err := audit.Write(ctx, tx, tenant, audit.Entry{ActorKind: e.kind, ActorID: &actor,
				Action: "user.login", SourceIPHash: e.hash}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("write audit events: %v", err)
	}

	svc := NewService(app)
	got, err := svc.UserActivity(ctx, tenant, alice)
	if err != nil {
		t.Fatal(err)
	}
	var want []UserActivityIP
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, legacyUserActivityIPsSQL, tenant, alice)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var st UserActivityIP
			if err := rows.Scan(&st.SourceIPEnc, &st.Count, &st.First, &st.Last, &st.Accounts, &st.AccountIDs); err != nil {
				return err
			}
			want = append(want, st)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("legacy profile ips: %v", err)
	}
	// 各来源次数互不相同（4、2、1），按次数排齐再比
	byCount := func(s []UserActivityIP) {
		sort.Slice(s, func(i, j int) bool { return s[i].Count > s[j].Count })
	}
	byCount(got.IPs)
	byCount(want)
	if !reflect.DeepEqual(got.IPs, want) {
		t.Fatalf("profile ips differ from legacy\nnew:    %+v\nlegacy: %+v", got.IPs, want)
	}
	// 不是两边都空：h1 三个账号同源（admin 身份那条不多算），h3 两个，h2 只有 alice 自己
	if len(got.IPs) != 3 || got.IPs[0].Count != 4 || got.IPs[0].Accounts != 3 || len(got.IPs[0].AccountIDs) != 3 {
		t.Fatalf("profile ips = %+v", got.IPs)
	}
	accounts := map[int]bool{}
	for _, ip := range got.IPs {
		accounts[ip.Accounts] = true
	}
	if !accounts[1] || !accounts[2] || !accounts[3] {
		t.Fatalf("profile ip account counts = %+v", got.IPs)
	}
	t.Log("marker=user_activity_ip_clusters_match_legacy_ok")
}
