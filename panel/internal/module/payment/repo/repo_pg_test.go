package repo

// 通过 Repo API 验证 payment 表约束与读写行为（渠道 jsonb、意图唯一、事件防删、收款唯一与 CHECK）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// pgCode 取约束违例的 SQLSTATE，取不到返回空串。
func pgCode(err error) string {
	v, _ := db.AsViolation(err)
	return v.Code
}

func mkUser(t *testing.T, g *gorm.DB) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	email := fmt.Sprintf("payment-%s@example.test", uuid.NewString())
	if err := g.Raw(`INSERT INTO users (email) VALUES (?) RETURNING id`, email).Row().Scan(&id); err != nil {
		t.Fatalf("建用户: %v", err)
	}
	return id
}

// newOrder 插一张待付的充值单（9.90 CNY，与 baseIntent / basePayment 的金额一致），返回 ID 与订单号：
// 意图、收款、挂账的 order_id 都有指向 orders 的外键。订单号用随机串，满足 ^[A-Z0-9-]{1,64}$ 且全局唯一。
func newOrder(t *testing.T, g *gorm.DB, user uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	var id uuid.UUID
	no := "T-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	err := g.Raw(`INSERT INTO orders (order_no, user_id, kind, currency, subtotal_amount, total_amount, payable_amount, expires_at)
		VALUES (?, ?, 'topup', 'CNY', 9.90, 9.90, 9.90, now() + interval '30 minutes') RETURNING id`, no, user).Row().Scan(&id)
	if err != nil {
		t.Fatalf("建订单: %v", err)
	}
	return id, no
}

// inSavepoint 在保存点里执行预期会失败的语句，执行完回滚到保存点：PG 事务里一条语句失败后整个事务作废，
// 不隔开的话后面的断言全会变成「current transaction is aborted」。
func inSavepoint(t *testing.T, g *gorm.DB, fn func() error) error {
	t.Helper()
	if err := g.Exec("SAVEPOINT expect_fail").Error; err != nil {
		t.Fatalf("开保存点: %v", err)
	}
	err := fn()
	if rerr := g.Exec("ROLLBACK TO SAVEPOINT expect_fail").Error; rerr != nil {
		t.Fatalf("回滚到保存点: %v", rerr)
	}
	return err
}

func newRepo(t *testing.T) (*Repo, context.Context, *gorm.DB) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	return New(d), ctx, d.Conn(ctx)
}

func insertProvider(t *testing.T, r *Repo, ctx context.Context, code string, min string, enabled, accepting bool, methods []string) uuid.UUID {
	t.Helper()
	return insertProviderAdapter(t, r, ctx, code, "epay", min, enabled, accepting, methods)
}

func insertProviderAdapter(t *testing.T, r *Repo, ctx context.Context, code, adapter, min string, enabled, accepting bool, methods []string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	p := &model.Provider{
		ID:           id,
		Code:         code,
		Adapter:      adapter,
		DisplayName:  "测试渠道",
		Currencies:   []string{"CNY", "USD"},
		BaseURL:      "https://checkout.example.test",
		Methods:      methods,
		MinAmount:    decimal.RequireFromString(min),
		Enabled:      enabled,
		AcceptingNew: accepting,
	}
	if err := r.InsertProvider(ctx, p); err != nil {
		t.Fatalf("InsertProvider %s: %v", code, err)
	}
	return id
}

func baseIntent(orderID, userID, providerID uuid.UUID, orderNo, outTradeNo string) *model.Intent {
	return &model.Intent{
		ID:          uuid.Must(uuid.NewV7()),
		OrderID:     orderID,
		OrderNo:     orderNo,
		UserID:      userID,
		ProviderID:  providerID,
		OutTradeNo:  outTradeNo,
		Currency:    "CNY",
		Amount:      decimal.RequireFromString("9.90"),
		Status:      "requires_action",
		HTTPMethod:  "GET",
		RedirectURL: "https://checkout.example.test/pay",
		FormFields:  map[string]string{},
	}
}

func basePayment(orderID, userID, providerID uuid.UUID, providerPayID string) *model.Payment {
	return &model.Payment{
		ID:                uuid.Must(uuid.NewV7()),
		OrderID:           orderID,
		UserID:            userID,
		ProviderID:        providerID,
		ProviderPaymentID: providerPayID,
		Currency:          "CNY",
		Amount:            decimal.RequireFromString("9.90"),
		FeeAmount:         decimal.Zero,
		Status:            "succeeded",
	}
}

// 渠道 jsonb 往返、methods CHECK、code 唯一。
func TestProviderJsonbAndUniqueness(t *testing.T) {
	r, ctx, g := newRepo(t)
	code := "pgp_" + uuid.NewString()[:8]

	id := insertProvider(t, r, ctx, code, "0", true, true, []string{"alipay"})
	got, ok, err := r.ProviderByCode(ctx, code)
	if err != nil || !ok {
		t.Fatalf("ProviderByCode: err=%v ok=%v", err, ok)
	}
	if got.ID != id || len(got.Currencies) != 2 || got.Currencies[0] != "CNY" || got.Currencies[1] != "USD" {
		t.Fatalf("currencies 往返不对: %+v", got.Currencies)
	}
	if len(got.Methods) != 1 || got.Methods[0] != "alipay" {
		t.Fatalf("methods 往返不对: %+v", got.Methods)
	}

	bad := &model.Provider{
		ID:           uuid.Must(uuid.NewV7()),
		Code:         "pgp_bad_" + uuid.NewString()[:6],
		Adapter:      "epay",
		DisplayName:  "坏渠道",
		Currencies:   []string{"CNY"},
		BaseURL:      "https://checkout.example.test",
		Methods:      []string{"paypal"},
		MinAmount:    decimal.Zero,
		Enabled:      true,
		AcceptingNew: true,
	}
	if err := inSavepoint(t, g, func() error { return r.InsertProvider(ctx, bad) }); !errors.Is(err, ErrCheck) {
		t.Fatalf("methods 含 paypal: want ErrCheck, got %v", err)
	}

	dup := &model.Provider{
		ID:           uuid.Must(uuid.NewV7()),
		Code:         code,
		Adapter:      "epay",
		DisplayName:  "重复",
		Currencies:   []string{"CNY"},
		BaseURL:      "https://checkout.example.test",
		Methods:      []string{"alipay"},
		MinAmount:    decimal.Zero,
		Enabled:      true,
		AcceptingNew: true,
	}
	if err := inSavepoint(t, g, func() error { return r.InsertProvider(ctx, dup) }); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("同 code: want ErrDuplicate, got %v", err)
	}
}

// 收新单渠道里最小的 min_amount；本事务内全部停收后 found=false（若库里另有已提交的收单渠道，最小值可能仍 found）。
func TestMinAcceptingAmount(t *testing.T) {
	r, ctx, _ := newRepo(t)
	hiCode := "pgp_hi_" + uuid.NewString()[:6]
	loCode := "pgp_lo_" + uuid.NewString()[:6]
	hiID := insertProvider(t, r, ctx, hiCode, "1.00", true, true, []string{"alipay"})
	loID := insertProvider(t, r, ctx, loCode, "0.50", true, true, []string{"alipay"})
	// 只有在线渠道（epay）算数：线下渠道不参与最低额
	insertProviderAdapter(t, r, ctx, "pgp_off_"+uuid.NewString()[:6], "offline", "0.02", true, true, nil)

	min, found, err := r.MinAcceptingAmount(ctx, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	wantLo := decimal.RequireFromString("0.50")
	if !found || min.GreaterThan(wantLo) {
		t.Fatalf("want min <= 0.50 among accepting (含本事务两渠道), got %s found=%v", min, found)
	}
	if min.Equal(decimal.RequireFromString("0.02")) {
		t.Fatal("线下渠道不应参与最低额")
	}
	if !min.Equal(wantLo) {
		// 共享库上可能有更低 min 的已提交渠道，只断言本事务插入的 0.50 在候选里
		t.Logf("全局 MIN 为 %s（非仅本用例两渠道）", min)
	}

	_, err = r.UpdateProviderFields(ctx, hiID, map[string]any{"accepting_new": false})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.UpdateProviderFields(ctx, loID, map[string]any{"accepting_new": false})
	if err != nil {
		t.Fatal(err)
	}
	min2, found2, err := r.MinAcceptingAmount(ctx, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if found2 && min2.Equal(wantLo) {
		t.Fatalf("两渠道停收后仍得到本用例的 0.50: found=%v min=%s", found2, min2)
	}
}

// 同订单两个 requires_action、状态迁移、CloseOrderIntents 排除、OutTradeNo 查询。
func TestIntentConstraints(t *testing.T) {
	r, ctx, g := newRepo(t)
	user := mkUser(t, g)
	prov := insertProvider(t, r, ctx, "pgp_int_"+uuid.NewString()[:6], "0", true, true, []string{"alipay"})
	orderID, orderNo := newOrder(t, g, user)

	it1 := baseIntent(orderID, user, prov, orderNo, orderNo)
	if err := r.InsertIntent(ctx, it1); err != nil {
		t.Fatal(err)
	}
	it2 := baseIntent(orderID, user, prov, orderNo, orderNo+"-2")
	if err := inSavepoint(t, g, func() error { return r.InsertIntent(ctx, it2) }); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("第二笔在途意图: want ErrDuplicate, got %v", err)
	}

	ok, err := r.SetIntentStatus(ctx, it1.ID, "requires_action", "succeeded")
	if err != nil || !ok {
		t.Fatalf("SetIntentStatus 成功: ok=%v err=%v", ok, err)
	}
	ok, err = r.SetIntentStatus(ctx, it1.ID, "requires_action", "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("旧状态不对时 SetIntentStatus 应为 false")
	}

	orderB, orderNoB := newOrder(t, g, user)
	keep := baseIntent(orderB, user, prov, orderNoB, orderNoB)
	if err := r.InsertIntent(ctx, keep); err != nil {
		t.Fatal(err)
	}
	ok, err = r.SetIntentStatus(ctx, keep.ID, "requires_action", "succeeded")
	if err != nil || !ok {
		t.Fatalf("先结束 keep 才能在同单再开在途: ok=%v err=%v", ok, err)
	}
	other := baseIntent(orderB, user, prov, orderNoB, orderNoB+"-2")
	if err := r.InsertIntent(ctx, other); err != nil {
		t.Fatal(err)
	}
	// except 指向已 succeeded 的 keep，只应关掉在途的 other（同单不能两笔 requires_action）
	n, err := r.CloseOrderIntents(ctx, orderB, &keep.ID, "requires_action", "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("CloseOrderIntents except: want 1 row, got %d", n)
	}
	gotKeep, ok, err := r.IntentByOutTradeNo(ctx, prov, orderNoB)
	if err != nil || !ok || gotKeep.Status != "succeeded" {
		t.Fatalf("keep 应仍为 succeeded: %+v ok=%v err=%v", gotKeep.Status, ok, err)
	}
	gotOther, ok, err := r.IntentByOutTradeNo(ctx, prov, orderNoB+"-2")
	if err != nil || !ok || gotOther.Status != "cancelled" {
		t.Fatalf("other 应已取消: %+v ok=%v err=%v", gotOther.Status, ok, err)
	}

	byNo, ok, err := r.IntentByOutTradeNo(ctx, prov, orderNo)
	if err != nil || !ok || byNo.ID != it1.ID {
		t.Fatalf("IntentByOutTradeNo: id=%v want=%v ok=%v err=%v", byNo.ID, it1.ID, ok, err)
	}
}

// 事件 (provider, event_id) 幂等插入；payment_events 禁止 DELETE。
func TestPaymentEvents(t *testing.T) {
	r, ctx, g := newRepo(t)
	prov := insertProvider(t, r, ctx, "pgp_ev_"+uuid.NewString()[:6], "0", true, true, []string{"alipay"})
	eventID := "evt-" + uuid.NewString()
	ev := &model.Event{
		ID:                uuid.Must(uuid.NewV7()),
		ProviderID:        prov,
		ProviderEventID:   eventID,
		EventType:         "payment.succeeded",
		SignatureVerified: true,
		ProcessingStatus:  "pending",
		RawPayload:        map[string]string{"k": "v"},
	}
	ins, err := r.InsertEvent(ctx, ev)
	if err != nil || !ins {
		t.Fatalf("第一次 InsertEvent: ins=%v err=%v", ins, err)
	}
	ev2 := &model.Event{
		ID:                uuid.Must(uuid.NewV7()),
		ProviderID:        prov,
		ProviderEventID:   eventID,
		EventType:         "payment.succeeded",
		SignatureVerified: true,
		ProcessingStatus:  "pending",
		RawPayload:        map[string]string{},
	}
	ins, err = r.InsertEvent(ctx, ev2)
	if err != nil {
		t.Fatal(err)
	}
	if ins {
		t.Fatal("同 (provider, event_id) 第二次应 inserted=false")
	}

	err = g.Exec(`DELETE FROM payment_events WHERE provider_id = ?`, prov).Error
	if pgCode(err) != "23001" {
		t.Fatalf("DELETE payment_events: want 23001, got code=%s err=%v", pgCode(err), err)
	}
}

// 收款 (provider, provider_payment_id) 唯一；fee 不得超过 amount。
func TestPayments(t *testing.T) {
	r, ctx, g := newRepo(t)
	user := mkUser(t, g)
	prov := insertProvider(t, r, ctx, "pgp_pay_"+uuid.NewString()[:6], "0", true, true, []string{"alipay"})
	orderID, _ := newOrder(t, g, user)
	payRef := "pay-" + uuid.NewString()

	p1 := basePayment(orderID, user, prov, payRef)
	if err := r.InsertPayment(ctx, p1); err != nil {
		t.Fatal(err)
	}
	p2 := basePayment(orderID, user, prov, payRef)
	if err := inSavepoint(t, g, func() error { return r.InsertPayment(ctx, p2) }); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("重复 provider_payment_id: want ErrDuplicate, got %v", err)
	}

	badFee := basePayment(orderID, user, prov, "pay-fee-"+uuid.NewString())
	badFee.FeeAmount = decimal.RequireFromString("10.00")
	badFee.Amount = decimal.RequireFromString("9.90")
	if err := inSavepoint(t, g, func() error { return r.InsertPayment(ctx, badFee) }); !errors.Is(err, ErrCheck) {
		t.Fatalf("fee > amount: want ErrCheck, got %v", err)
	}
}

// payments 只追加：UPDATE、DELETE 都被拒（23001）。
func TestPaymentsAppendOnly(t *testing.T) {
	r, ctx, g := newRepo(t)
	user := mkUser(t, g)
	prov := insertProvider(t, r, ctx, "pgp_ao_"+uuid.NewString()[:6], "0", true, true, []string{"alipay"})
	orderID, _ := newOrder(t, g, user)
	p := basePayment(orderID, user, prov, "pay-ao-"+uuid.NewString())
	if err := r.InsertPayment(ctx, p); err != nil {
		t.Fatal(err)
	}
	err := inSavepoint(t, g, func() error {
		return g.Exec(`UPDATE payments SET amount = 1.00 WHERE id = ?`, p.ID).Error
	})
	if pgCode(err) != "23001" {
		t.Fatalf("UPDATE payments: want 23001, got code=%s err=%v", pgCode(err), err)
	}
	err = inSavepoint(t, g, func() error {
		return g.Exec(`DELETE FROM payments WHERE id = ?`, p.ID).Error
	})
	if pgCode(err) != "23001" {
		t.Fatalf("DELETE payments: want 23001, got code=%s err=%v", pgCode(err), err)
	}
}

// ProviderLastEvents 只看验过签的事件；PurgeUnverifiedEvents 只删 30 天以前、验签失败的事件。
func TestUnverifiedEventsLastSeenAndPurge(t *testing.T) {
	r, ctx, g := newRepo(t)
	prov := insertProvider(t, r, ctx, "pgp_pu_"+uuid.NewString()[:6], "0", true, true, []string{"alipay"})
	ins := func(eventID string, verified bool, age string) {
		t.Helper()
		status, processed := "pending", "NULL"
		if !verified {
			status, processed = "ignored", "now()"
		}
		q := `INSERT INTO payment_events (provider_id, provider_event_id, event_type, signature_verified, processing_status, processed_at, received_at)
VALUES (?, ?, 'payment.unknown', ?, ?, ` + processed + `, now() - ?::interval)`
		if err := g.Exec(q, prov, eventID, verified, status, age).Error; err != nil {
			t.Fatalf("insert %s: %v", eventID, err)
		}
	}
	ins("ok-old", true, "40 days")
	ins("ok-recent", true, "2 hours")
	ins("unverified:recent", false, "1 hour")
	ins("unverified:old", false, "40 days")

	var okRecentAt time.Time
	if err := g.Raw(`SELECT received_at FROM payment_events WHERE provider_id = ? AND provider_event_id = 'ok-recent'`, prov).
		Scan(&okRecentAt).Error; err != nil {
		t.Fatal(err)
	}
	last, err := r.ProviderLastEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := last[prov]; !ok || !got.Equal(okRecentAt) {
		t.Fatalf("最近回调应是验过签的 %v，得 %v（ok=%v）", okRecentAt, got, ok)
	}

	remaining := func() map[string]bool {
		t.Helper()
		var ids []string
		if err := g.Raw(`SELECT provider_event_id FROM payment_events WHERE provider_id = ?`, prov).Scan(&ids).Error; err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}

	// before 早于 40 天前：什么都不删
	if _, err := r.PurgeUnverifiedEvents(ctx, time.Now().AddDate(0, 0, -60), 100); err != nil {
		t.Fatal(err)
	}
	if !remaining()["unverified:old"] {
		t.Fatal("before 之后的事件不应删")
	}
	// before 是现在：库函数仍只删 30 天以前的
	n, err := r.PurgeUnverifiedEvents(ctx, time.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("应删掉至少 1 行，得 %d", n)
	}
	left := remaining()
	if left["unverified:old"] {
		t.Fatal("30 天以前验签失败的事件应删")
	}
	for _, id := range []string{"unverified:recent", "ok-old", "ok-recent"} {
		if !left[id] {
			t.Fatalf("%s 不应删", id)
		}
	}
}
