// [INPUT]: 依赖 platform/pg18test 打开 catalog_sales 域的一次性库，依赖 users.go 的 ListUsers / GetUser、bulk_users.go 的 PreviewBulk、bulk_mail.go 的 SendBulkMail，依赖 platform/crypto 的 HashToken
// [OUTPUT]: 对外提供 TestAdminUsersFiltersAndFieldsPG18
// [POS]: domain/adminops 的 PG18 测试（契约后台-03）：用户列表的状态多值、用户组、订阅状态与 q（id / 订阅令牌）筛选与当前订阅摘要，详情的配额、设备、统计（paid_totals 按币种拆开，R80）、邀请人与 Telegram；批量筛选的套餐、到期、订阅状态与样本行，群发正文变量替换
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"reflect"
	"sort"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestAdminUsersFiltersAndFieldsPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant  = "7a100000-0000-4000-8000-000000000001"
		group   = "7a100000-0000-4000-8000-000000000002"
		product = "7a100000-0000-4000-8000-000000000003"
		plan    = "7a100000-0000-4000-8000-000000000004"
		version = "7a100000-0000-4000-8000-000000000005"
		alice   = "7a100000-0000-4000-8000-000000000011" // 在用订阅、组内、被 bob 邀请、绑了 Telegram
		bob     = "7a100000-0000-4000-8000-000000000012" // 封禁、没有订阅
		carol   = "7a100000-0000-4000-8000-000000000013" // 停用、订阅已过期
		subA    = "7a100000-0000-4000-8000-000000000021"
		subC    = "7a100000-0000-4000-8000-000000000023"
		token   = "a1ice-subscription-token-7a1000000000000000"
	)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`SET LOCAL session_replication_role = replica`, nil},
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'admin-users-filters','Filters','CNY')`, []any{tenant}},
		{`INSERT INTO user_groups(id,tenant_id,code,name) VALUES($2,$1,'vip','贵宾组')`, []any{tenant, group}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status,user_group_id) VALUES($2,$1,'alice@filters.invalid','Alice','active',$3)`, []any{tenant, alice, group}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'bob@filters.invalid','Bob','banned')`, []any{tenant, bob}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'carol@filters.invalid','Carol','suspended')`, []any{tenant, carol}},
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'fp','Filters Product','active')`, []any{tenant, product}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'pro','Pro 月付','active')`, []any{tenant, product, plan}},
		{`INSERT INTO plan_versions(id,tenant_id,plan_id,version,max_devices) VALUES($3,$1,$2,1,3)`, []any{tenant, plan, version}},
		{`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
			current_period_start,current_period_end,created_at)
		  VALUES($2,$1,$3,$4,$5,'active','CNY',3000,now()-interval '5 days',now()+interval '25 days',now()-interval '5 days'),
		        ($6,$1,$7,$4,$5,'expired','CNY',3000,now()-interval '60 days',now()-interval '30 days',now()-interval '60 days')`,
			[]any{tenant, subA, alice, plan, version, subC, carol}},
		{`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
		  VALUES($1,$2,'traffic.bytes','cycle',now()-interval '5 days',now()+interval '25 days',100,100,40)`, []any{tenant, subA}},
		{`INSERT INTO subscription_credentials(tenant_id,subscription_id,user_id,token_hash,token_prefix,status)
		  VALUES($1,$2,$3,$4,$5,'active')`, []any{tenant, subA, alice, crypto.HashToken(token), token[:8]}},
		{`INSERT INTO referrals(referee_user_id,tenant_id,referrer_user_id) VALUES($2,$1,$3)`, []any{tenant, alice, bob}},
		{`INSERT INTO telegram_bindings(tenant_id,user_id,chat_id,username) VALUES($1,$2,42,'alice_tg')`, []any{tenant, alice}},
		// carol 的实收跨两个币种（R80）：待支付单不计，0 元赠送单不出现在拆分里
		{`INSERT INTO orders(tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,discount_amount,tax_amount,
			total_amount,balance_applied,payable_amount,paid_amount,paid_at,expires_at,business_request_id)
		  VALUES($1,'UF-CNY-1',$2,'new','fulfilled','CNY',3000,0,0,3000,0,3000,3000,now(),now(),gen_random_uuid()),
		        ($1,'UF-CNY-2',$2,'renewal','paid','CNY',1200,0,0,1200,0,1200,1200,now(),now(),gen_random_uuid()),
		        ($1,'UF-USD-1',$2,'new','fulfilled','USD',500,0,0,500,0,500,500,now(),now(),gen_random_uuid()),
		        ($1,'UF-CNY-3',$2,'new','pending_payment','CNY',900,0,0,900,0,900,0,NULL,now()+interval '30 minutes',gen_random_uuid()),
		        ($1,'UF-GBP-0',$2,'new','fulfilled','GBP',0,0,0,0,0,0,0,now(),now(),gen_random_uuid())`,
			[]any{tenant, carol}},
	} {
		if _, err := tx.Exec(ctx, row.sql, row.args...); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, row.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	svc := NewService(app)
	emails := func(in ListUsersInput) []string {
		t.Helper()
		in.Limit = 50
		rows, total, err := svc.ListUsers(ctx, tenant, in)
		if err != nil || int(total) != len(rows) {
			t.Fatalf("list %+v: rows=%d total=%d err=%v", in, len(rows), total, err)
		}
		out := []string{}
		for _, r := range rows {
			out = append(out, r.Email)
		}
		sort.Strings(out)
		return out
	}
	for name, tc := range map[string]struct {
		in   ListUsersInput
		want []string
	}{
		"status multi":       {ListUsersInput{Status: "suspended, banned"}, []string{"bob@filters.invalid", "carol@filters.invalid"}},
		"status exact":       {ListUsersInput{Status: "act"}, []string{}},
		"group":              {ListUsersInput{GroupID: group}, []string{"alice@filters.invalid"}},
		"no group":           {ListUsersInput{GroupID: "none"}, []string{"bob@filters.invalid", "carol@filters.invalid"}},
		"sub active":         {ListUsersInput{SubState: "active"}, []string{"alice@filters.invalid"}},
		"sub expired":        {ListUsersInput{SubState: "expired"}, []string{"carol@filters.invalid"}},
		"sub none":           {ListUsersInput{SubState: "none"}, []string{"bob@filters.invalid"}},
		"q email":            {ListUsersInput{Query: "CAROL"}, []string{"carol@filters.invalid"}},
		"q id":               {ListUsersInput{Query: bob}, []string{"bob@filters.invalid"}},
		"q token":            {ListUsersInput{Query: token}, []string{"alice@filters.invalid"}},
		"q subscription url": {ListUsersInput{Query: "https://sub.example/s/" + token + "?flag=clash"}, []string{"alice@filters.invalid"}},
	} {
		if got := emails(tc.in); len(got) != len(tc.want) || (len(got) > 0 && !equalStrings(got, tc.want)) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
	if _, _, err := svc.ListUsers(ctx, tenant, ListUsersInput{SubState: "gone", GroupID: "vip"}); err == nil {
		t.Fatal("invalid sub_state / group_id accepted")
	}

	rows, _, err := svc.ListUsers(ctx, tenant, ListUsersInput{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byEmail := map[string]UserRow{}
	for _, r := range rows {
		byEmail[r.Email] = r
	}
	a := byEmail["alice@filters.invalid"]
	if a.GroupID == nil || *a.GroupID != group || a.CurrentSubscription == nil {
		t.Fatalf("alice row = %+v", a)
	}
	cs := a.CurrentSubscription
	if cs.ID != subA || cs.PlanName != "Pro 月付" || cs.Status != "active" || cs.Traffic.Limit == nil ||
		*cs.Traffic.Limit != 100 || cs.Traffic.Consumed != 40 || cs.DeviceLimit != 3 || cs.OnlineDevices != 0 {
		t.Fatalf("alice current subscription = %+v", cs)
	}
	// 没有在用订阅时取最近一条（过期的也显示）；没有订阅为 null
	if c := byEmail["carol@filters.invalid"].CurrentSubscription; c == nil || c.ID != subC || c.Status != "expired" || c.Traffic.Limit != nil {
		t.Fatalf("carol current subscription = %+v", c)
	}
	if byEmail["bob@filters.invalid"].CurrentSubscription != nil || byEmail["bob@filters.invalid"].GroupID != nil {
		t.Fatalf("bob row = %+v", byEmail["bob@filters.invalid"])
	}

	d, err := svc.GetUser(ctx, tenant, alice)
	if err != nil {
		t.Fatal(err)
	}
	if d.GroupID == nil || *d.GroupID != group || d.Referrer == nil || d.Referrer.ID != bob ||
		d.Telegram == nil || d.Telegram.Username != "alice_tg" || !reflect.DeepEqual(d.Stats, UserStats{PaidTotals: []CurrencyAmount{}}) ||
		len(d.Subscriptions) != 1 {
		t.Fatalf("alice detail = %+v", d)
	}
	sub := d.Subscriptions[0]
	if sub.CurrentPeriodStart == nil || sub.DeviceLimitOverride != nil || sub.PlanMaxDevices == nil || *sub.PlanMaxDevices != 3 ||
		len(sub.Quotas) != 1 || sub.Quotas[0].Metric != "traffic.bytes" || sub.Quotas[0].Remaining == nil || *sub.Quotas[0].Remaining != 60 {
		t.Fatalf("alice subscription detail = %+v", sub)
	}
	if bd, err := svc.GetUser(ctx, tenant, bob); err != nil || bd.Stats.ReferralCount != 1 || bd.Referrer != nil || bd.Telegram != nil {
		t.Fatalf("bob detail = %+v err=%v", bd, err)
	}
	// R80：旧字段仍是跨币种直接相加，paid_totals 按币种拆开、币种升序
	if cd, err := svc.GetUser(ctx, tenant, carol); err != nil || cd.Stats.PaidTotal != 4700 || cd.Stats.OrderCount != 5 ||
		!reflect.DeepEqual(cd.Stats.PaidTotals, []CurrencyAmount{{"CNY", 4200}, {"USD", 500}}) {
		t.Fatalf("carol stats = %+v err=%v", cd.Stats, err)
	}
	if _, err := svc.GetUser(ctx, tenant, "not-a-uuid"); err == nil {
		t.Fatal("non-uuid user id did not fail")
	}

	// 批量筛选：套餐、到期天数、订阅状态按「当前订阅」，预览、导出、群发共用
	preview := func(f BulkFilter) []string {
		t.Helper()
		p, err := svc.PreviewBulk(ctx, tenant, f)
		if err != nil || p.Total != len(p.Samples) || len(p.SampleRows) != len(p.Samples) {
			t.Fatalf("preview %+v: %+v err=%v", f, p, err)
		}
		sort.Strings(p.Samples)
		return p.Samples
	}
	for name, tc := range map[string]struct {
		f    BulkFilter
		want []string
	}{
		"plan":            {BulkFilter{PlanID: plan}, []string{"alice@filters.invalid", "carol@filters.invalid"}},
		"plan live":       {BulkFilter{PlanID: plan, SubState: "active"}, []string{"alice@filters.invalid"}},
		"expires 30 days": {BulkFilter{ExpiresWithinDays: 30}, []string{"alice@filters.invalid"}},
		"expires 10 days": {BulkFilter{ExpiresWithinDays: 10}, []string{}},
		"sub none":        {BulkFilter{SubState: "none"}, []string{"bob@filters.invalid"}},
	} {
		if got := preview(tc.f); !equalStrings(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("preview %s: got %v, want %v", name, got, tc.want)
		}
	}
	p, err := svc.PreviewBulk(ctx, tenant, BulkFilter{PlanID: plan, SubState: "active"})
	if err != nil || len(p.SampleRows) != 1 || p.SampleRows[0].PlanName == nil || *p.SampleRows[0].PlanName != "Pro 月付" ||
		p.SampleRows[0].CurrentPeriodEnd == nil {
		t.Fatalf("sample rows=%+v err=%v", p, err)
	}
	for _, bad := range []BulkFilter{{PlanID: "x"}, {ExpiresWithinDays: 366}, {SubState: "gone"}} {
		if _, err := svc.PreviewBulk(ctx, tenant, bad); err == nil {
			t.Errorf("invalid filter %+v accepted", bad)
		}
	}

	// 群发：正文变量逐人替换；到期按站点时区（默认 Asia/Shanghai）写日期
	res, err := svc.SendBulkMail(ctx, tenant, BulkMailInput{Filter: BulkFilter{PlanID: plan},
		Subject: "续费提醒", Body: "$email 的 $plan 于 $expire 到期", ActorID: bob})
	if err != nil || res.Queued != 2 {
		t.Fatalf("bulk mail=%+v err=%v", res, err)
	}
	var aliceBody, carolBody, wantDate string
	if err := admin.QueryRow(ctx, `SELECT to_char(current_period_end AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD')
		FROM subscriptions WHERE id=$1`, subA).Scan(&wantDate); err != nil {
		t.Fatal(err)
	}
	for user, dst := range map[string]*string{alice: &aliceBody, carol: &carolBody} {
		if err := admin.QueryRow(ctx, `SELECT payload->>'body' FROM notification_deliveries
			WHERE tenant_id=$1 AND user_id=$2 AND template_code='admin.broadcast'`, tenant, user).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if aliceBody != "alice@filters.invalid 的 Pro 月付 于 "+wantDate+" 到期" {
		t.Fatalf("alice body=%q (want date %s)", aliceBody, wantDate)
	}
	if carolBody[:len("carol@filters.invalid 的 Pro 月付 于 ")] != "carol@filters.invalid 的 Pro 月付 于 " {
		t.Fatalf("carol body=%q", carolBody)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
