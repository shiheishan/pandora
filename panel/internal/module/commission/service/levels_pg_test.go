package service

import (
	"context"
	"database/sql"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// levelRow 是一单佣金条目里测试关心的字段。
type levelRow struct {
	Referrer       uuid.UUID
	Level          int
	Share          int
	Amount         decimal.Decimal
	Status         string
	ReviewRequired bool
	RateBP         int64
}

// setDistribution 以管理员身份设多级分销开关与三级份额（百分比）。
func setDistribution(t *testing.T, e *env, ctx context.Context, admin uuid.UUID, on bool, l1, l2, l3 int) {
	t.Helper()
	if err := e.settings.SetCommissionDistribution(adminCtx(ctx, admin), settingssvc.CommissionDistributionUpdate{
		Enabled: &on, L1: &l1, L2: &l2, L3: &l3,
	}); err != nil {
		t.Fatal(err)
	}
}

// entriesOf 按 level 升序读这一单的佣金条目。
func entriesOf(t *testing.T, e *env, ctx context.Context, orderID uuid.UUID) []levelRow {
	t.Helper()
	var out []levelRow
	for lvl := 1; lvl <= MaxCommissionLevels; lvl++ {
		var r levelRow
		err := e.row(ctx, `SELECT referrer_user_id, level, share_percent, commission_amount, status, review_required, rate_bp
			FROM commission_entries WHERE order_id = ? AND level = ?`, orderID, lvl).
			Scan(&r.Referrer, &r.Level, &r.Share, &r.Amount, &r.Status, &r.ReviewRequired, &r.RateBP)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			t.Fatalf("读 level %d: %v", lvl, err)
		}
		if r.Referrer == uuid.Nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

func entryIDOf(t *testing.T, e *env, ctx context.Context, orderID uuid.UUID, referrer uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `SELECT id FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, orderID, referrer).
		Scan(&id); err != nil {
		t.Fatalf("读条目 id: %v", err)
	}
	return id
}

func TestLevelsOffDirectGetsAll(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	buyer, a, b := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 1 {
		t.Fatalf("条目 %d 行，应只有直接邀请人一条", len(rows))
	}
	r := rows[0]
	if r.Referrer != a || r.Level != 1 || r.Share != 100 || !r.Amount.Equal(dec2("10.00")) || r.Status != EntryPending {
		t.Fatalf("条目不对: %+v", r)
	}
	if bal := accountBalance(t, e, ctx, b, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("B 待结科目 %s，应为 0", bal)
	}
}

func TestLevelsSplitThree(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a, b, c := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, c)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 3 {
		t.Fatalf("条目 %d 行", len(rows))
	}
	want := []struct {
		user  uuid.UUID
		lvl   int
		share int
		amt   string
	}{
		{a, 1, 50, "5.00"},
		{b, 2, 30, "3.00"},
		{c, 3, 20, "2.00"},
	}
	for i, w := range want {
		r := rows[i]
		if r.Referrer != w.user || r.Level != w.lvl || r.Share != w.share || !r.Amount.Equal(dec2(w.amt)) || r.Status != EntryPending {
			t.Fatalf("第 %d 条: %+v，应为 %v %d %d %s pending", i+1, r, w.user, w.lvl, w.share, w.amt)
		}
	}
	for _, w := range want {
		if bal := accountBalance(t, e, ctx, w.user, ledgersvc.UserCommissionPending); !bal.Equal(dec2(w.amt)) {
			t.Fatalf("%s 待结科目 %s", w.user, bal)
		}
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_accrued' AND source_id = ?`, o.OrderID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("计提交易 %d 笔 err=%v", n, err)
	}
}

func TestLevelsMissingLevels(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 1 || !rows[0].Amount.Equal(dec2("5.00")) {
		t.Fatalf("应只有 A 一条 5.00: %+v", rows)
	}
}

func TestLevelsLoopStops(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)

	buyer, a, b := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, a)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 2 {
		t.Fatalf("情况一应两条: %+v", rows)
	}
	if rows[0].Referrer != a || rows[0].Level != 1 || rows[1].Referrer != b || rows[1].Level != 2 {
		t.Fatalf("情况一级别不对: %+v", rows)
	}
	var nA int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, o.OrderID, a).Scan(&nA); err != nil || nA != 1 {
		t.Fatalf("A 应只出现一次: %d err=%v", nA, err)
	}

	t.Run("buyerInvitesDirect", func(t *testing.T) {
		buyer2, a2 := newUser(t, e, ctx), newUser(t, e, ctx)
		refer(e, buyer2, a2)
		refer(e, a2, buyer2)
		o2 := newPaidOrder(t, e, ctx, buyer2, "100.00")
		if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
			t.Fatal(err)
		}
		rows2 := entriesOf(t, e, ctx, o2.OrderID)
		if len(rows2) != 1 || rows2[0].Referrer != a2 || rows2[0].Level != 1 {
			t.Fatalf("情况二应只有 A L1: %+v", rows2)
		}
	})
}

func TestLevelsSharesSumTo100FourDeep(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 60, 30, 10)
	buyer, a, b, c, d := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, c)
	refer(e, c, d)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 3 {
		t.Fatalf("应三条: %+v", rows)
	}
	wantAmt := []string{"6.00", "3.00", "1.00"}
	sum := decimal.Zero
	for i, amt := range wantAmt {
		if !rows[i].Amount.Equal(dec2(amt)) {
			t.Fatalf("第 %d 条金额 %s", i+1, rows[i].Amount)
		}
		sum = sum.Add(rows[i].Amount)
	}
	if !sum.Equal(dec2("10.00")) {
		t.Fatalf("合计 %s", sum)
	}
	if bal := accountBalance(t, e, ctx, d, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("D 待结科目 %s", bal)
	}
	var nD int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, o.OrderID, d).Scan(&nD); err != nil || nD != 0 {
		t.Fatalf("D 不应有条目: %d err=%v", nD, err)
	}
}

func TestLevelsZeroShareSkipped(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 70, 0, 30)
	buyer, a, b, c := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, c)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 2 {
		t.Fatalf("应两条: %+v", rows)
	}
	if rows[0].Referrer != a || rows[0].Level != 1 || !rows[0].Amount.Equal(dec2("7.00")) {
		t.Fatalf("A L1: %+v", rows[0])
	}
	if rows[1].Referrer != c || rows[1].Level != 3 || !rows[1].Amount.Equal(dec2("3.00")) {
		t.Fatalf("C L3: %+v", rows[1])
	}
	var nB int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, o.OrderID, b).Scan(&nB); err != nil || nB != 0 {
		t.Fatalf("B 不应有条目: %d", nB)
	}
}

func TestLevelsTruncatePerLevel(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a, b, c := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, c)
	o := newPaidOrder(t, e, ctx, buyer, "33.33")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 3 {
		t.Fatalf("应三条: %+v", rows)
	}
	want := []string{"1.66", "0.99", "0.66"}
	for i, amt := range want {
		if !rows[i].Amount.Equal(dec2(amt)) {
			t.Fatalf("第 %d 级 %s，得 %s", i+1, amt, rows[i].Amount)
		}
	}
}

func TestLevelsSameSourceBlocksOneLevel(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a, b, c := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	refer(e, b, c)
	e.svc.UseSourceMatcher(&fakeSources{same: map[[2]uuid.UUID]bool{{b, buyer}: true}})
	defer e.svc.UseSourceMatcher(&fakeSources{})

	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{a, c} {
		var review bool
		if err := e.row(ctx, `SELECT review_required FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, o.OrderID, u).
			Scan(&review); err != nil || review {
			t.Fatalf("%s 不应待复核: review=%v err=%v", u, review, err)
		}
	}
	var reviewB bool
	if err := e.row(ctx, `SELECT review_required FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, o.OrderID, b).
		Scan(&reviewB); err != nil || !reviewB {
		t.Fatalf("B 应待复核: %v err=%v", reviewB, err)
	}

	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{a, c} {
		var st string
		if err := e.row(ctx, `SELECT status FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, o.OrderID, u).Scan(&st); err != nil || st != EntryAvailable {
			t.Fatalf("%s 应 available: %s err=%v", u, st, err)
		}
	}
	var stB string
	if err := e.row(ctx, `SELECT status FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, o.OrderID, b).Scan(&stB); err != nil || stB != EntryPending {
		t.Fatalf("B 应仍 pending: %s err=%v", stB, err)
	}

	idB := entryIDOf(t, e, ctx, o.OrderID, b)
	status, err := e.svc.ReviewEntry(adminCtx(ctx, admin), idB, false, "同源，作废")
	if err != nil || status != EntryVoided {
		t.Fatalf("作废 B: status=%s err=%v", status, err)
	}
	for _, u := range []uuid.UUID{a, c} {
		var st string
		if err := e.row(ctx, `SELECT status FROM commission_entries WHERE order_id = ? AND referrer_user_id = ?`, o.OrderID, u).Scan(&st); err != nil || st != EntryAvailable {
			t.Fatalf("作废后 %s 仍应 available: %s", u, st)
		}
	}
}

func TestLevelsReplayOnce(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a, b := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	for i := 0; i < 2; i++ {
		if err := e.svc.OnOrderPaid(ctx, o); err != nil {
			t.Fatalf("第 %d 次计提: %v", i+1, err)
		}
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 2 {
		t.Fatalf("应仍两条: %+v", rows)
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionPending); !bal.Equal(dec2("5.00")) {
		t.Fatalf("A 待结科目 %s", bal)
	}
}

func TestLevelsOverrideRateUsesDirect(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a, b := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)
	twenty := 20
	e.refs.mu.Lock()
	emailA := e.refs.emails[a]
	e.refs.mu.Unlock()
	if _, err := e.svc.SetOverride(adminCtx(ctx, admin), OverrideInput{Email: emailA, RatePercent: &twenty}); err != nil {
		t.Fatal(err)
	}
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	rows := entriesOf(t, e, ctx, o.OrderID)
	if len(rows) != 2 {
		t.Fatalf("应两条: %+v", rows)
	}
	if !rows[0].Amount.Equal(dec2("10.00")) || !rows[1].Amount.Equal(dec2("6.00")) {
		t.Fatalf("金额 %+v", rows)
	}
	for _, r := range rows {
		if r.RateBP != 2000 {
			t.Fatalf("rate_bp 应为 2000，得 %d", r.RateBP)
		}
	}
}

// 门户「付费好友」只算直接邀请（第 1 级）；第 2、3 级条目不给上级看下线邮箱；汇总带站点币种。
func TestMyCommissionLevelOneOnly(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "100.00", "every_order")
	setDistribution(t, e, ctx, admin, true, 50, 30, 20)
	buyer, a, b := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, a)
	refer(e, a, b)

	// buyer 下单：b 只拿到第 2 级
	o1 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o1); err != nil {
		t.Fatal(err)
	}
	mc, err := e.svc.MyCommission(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Summary.PaidInvitees != 0 || mc.Summary.Orders != 1 || mc.Summary.SiteCurrency != testCurrency {
		t.Fatalf("只有第 2 级条目时 %+v", mc.Summary)
	}
	if len(mc.Entries) != 1 || mc.Entries[0].Level != 2 || mc.Entries[0].From != "" {
		t.Fatalf("第 2 级条目不应带邮箱: %+v", mc.Entries)
	}

	// a 自己下单：b 拿到第 1 级，付费好友 1，这一条带打码邮箱
	o2 := newPaidOrder(t, e, ctx, a, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}
	mc, err = e.svc.MyCommission(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Summary.PaidInvitees != 1 || mc.Summary.Orders != 2 {
		t.Fatalf("加一条第 1 级后 %+v", mc.Summary)
	}
	for _, en := range mc.Entries {
		switch en.Level {
		case 1:
			if en.From == "" || en.From == "***" {
				t.Fatalf("第 1 级条目应带打码邮箱: %+v", en)
			}
		default:
			if en.From != "" {
				t.Fatalf("第 %d 级条目不应带邮箱: %+v", en.Level, en)
			}
		}
	}
}
