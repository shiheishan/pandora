package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	ledgerrepo "github.com/aegispanel/aegis/internal/module/ledger/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	paymentrepo "github.com/aegispanel/aegis/internal/module/payment/repo"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var paymentCommitTables = []string{
	"late_payment_cases", "payment_events", "payments", "payment_intents",
	"balance_holds", "ledger_entries", "ledger_transactions", "ledger_accounts",
}

// stubDirectory 代替 identity，批量邮箱供审计等使用。
type stubDirectory struct{}

func (stubDirectory) Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	for _, id := range ids {
		out[id] = "u-" + id.String()[:8] + "@example.test"
	}
	return out, nil
}

// stubSettler 模拟 order 结算：成功则记 order_paid，可配置 NotPayable。
// 返回 NotPayable 前先照常记一笔 order_paid（模拟真 Settler 写到一半才发现不可付），
// 用来核对 payment 在保存点上撤回了它的写入。
type stubSettler struct {
	mu         sync.Mutex
	users      map[uuid.UUID]uuid.UUID
	notPayable map[uuid.UUID]error
	calls      atomic.Int64
	ledger     *ledgersvc.Service
	last       SettleInput // 最近一次 SettlePayment 入参（测试断言用）
	// before 非空时在结算最前面调用（并发用例在这里拿订单锁）；返回错误即结算失败。
	before func(ctx context.Context, in SettleInput) error
}

func (s *stubSettler) SettlePayment(ctx context.Context, in SettleInput) error {
	s.calls.Add(1)
	s.mu.Lock()
	s.last = in
	before := s.before
	npErr, notPayable := s.notPayable[in.OrderID]
	userID := s.users[in.OrderID]
	s.mu.Unlock()
	if before != nil {
		if err := before(ctx, in); err != nil {
			return err
		}
	}
	if userID == uuid.Nil {
		return fmt.Errorf("payment test: 未登记订单 %s", in.OrderID)
	}
	_, err := s.ledger.PostOrderPaid(ctx, ledgersvc.OrderPaid{
		OrderID:   in.OrderID,
		UserID:    userID,
		Total:     in.Amount,
		Paid:      in.Amount,
		Fee:       in.Fee,
		Surcharge: in.Surcharge,
		Provider:  in.ProviderCode,
	})
	if err != nil {
		return err
	}
	if notPayable {
		return npErr
	}
	return nil
}

type env struct {
	svc     *Service
	row     func(ctx context.Context, q string, args ...any) *sql.Row
	exec    func(ctx context.Context, q string, args ...any) error
	settler *stubSettler
	ledger  *ledgersvc.Service
	// txm 给并发用例另开事务（只在 newCommitEnv 下有意义）。
	txm *tx.Manager
	// settings 用来改站点时区等设置。
	settings *settingssvc.Service
	// orders 记下 mkOrder 建的订单；真提交的用例结束时删掉它们。
	orders *orderLog
}

// orderLog 记下用例建的订单 ID（并发安全）。
type orderLog struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (l *orderLog) add(id uuid.UUID) {
	l.mu.Lock()
	l.ids = append(l.ids, id)
	l.mu.Unlock()
}

func (l *orderLog) take() []uuid.UUID {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := l.ids
	l.ids = nil
	return ids
}

// newEnv 的用例整例回滚（testkit.TxCtx）；newCommitEnv 的用例真提交（并发用），前后清表。
func newEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	return setupEnv(t, false)
}

func newCommitEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	return setupEnv(t, true)
}

// setupEnv 搭 payment 及其依赖（audit、settings、ledger）；service 测试不直接引用 db 包（depguard），连接类型靠推导。
func setupEnv(t *testing.T, commit bool) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	orders := &orderLog{}
	if commit {
		// 最先登记、最后执行：clean 截掉引用订单的意图、收款、挂账、冻结之后，再删本用例建的订单。
		// orders 有禁删触发器，在 session_replication_role = replica 的事务里删（测试库是超级用户）。
		t.Cleanup(func() {
			ids := orders.take()
			if len(ids) == 0 {
				return
			}
			g := d.Base().Begin()
			if g.Error != nil {
				t.Errorf("清订单: %v", g.Error)
				return
			}
			defer g.Rollback()
			if err := g.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
				t.Errorf("清订单: %v", err)
				return
			}
			if err := g.Exec(`DELETE FROM orders WHERE id IN ?`, ids).Error; err != nil {
				t.Errorf("清订单: %v", err)
				return
			}
			if err := g.Commit().Error; err != nil {
				t.Errorf("清订单: %v", err)
			}
		})
		clean := func() {
			testkit.Truncate(t, d, paymentCommitTables...)
			if err := d.Conn(context.Background()).Exec(`DELETE FROM payment_providers WHERE code <> 'offline'`).Error; err != nil {
				t.Fatal(err)
			}
		}
		clean()
		t.Cleanup(clean)
	}
	_, rdb := testkit.Redis(t)

	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x03
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	// settings 也要一份：payment 的回调地址读它；ledger service 不再依赖 settings。
	settings := settingssvc.New(settingsrepo.New(d), tx.NewManager(d), sealer, rdb, auditSvc)
	ledgerSvc := ledgersvc.New(ledgerrepo.New(d), tx.NewManager(d), auditSvc)
	svc := New(paymentrepo.New(d), tx.NewManager(d), ledgerSvc, nil, settings, auditSvc, sealer, Config{
		PublicBaseURL:     "https://panel.example.test",
		AllowPrivateHosts: true,
	})
	svc.identity = stubDirectory{}
	settler := &stubSettler{
		users:      map[uuid.UUID]uuid.UUID{},
		notPayable: map[uuid.UUID]error{},
		ledger:     ledgerSvc,
	}
	svc.UseSettler(settler)

	ctx := context.Background()
	if !commit {
		ctx = testkit.TxCtx(t, d)
	}
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.42", UserAgent: "payment-pg-test/1.0"})

	e := &env{
		svc:      svc,
		settler:  settler,
		ledger:   ledgerSvc,
		txm:      tx.NewManager(d),
		settings: settings,
		orders:   orders,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	// 测试渠道走替身，其余照常构造。
	e.svc.adapter = e.adapterWithFake
	return e, ctx
}

func (e *env) mkUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	email := fmt.Sprintf("payment-%s@example.test", uuid.New().String())
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) mkFakeProvider(t *testing.T, ctx context.Context, code string) providerRecord {
	t.Helper()
	if !strings.HasPrefix(code, fakeCodePrefix) {
		t.Fatalf("mkFakeProvider: 渠道代码须以 %q 开头，得 %q", fakeCodePrefix, code)
	}
	id := uuid.Must(uuid.NewV7())
	sealed, err := e.svc.sealCredentials(id, credentials{Key: fakeSecret})
	if err != nil {
		t.Fatal(err)
	}
	p := &model.Provider{
		ID:                id,
		Code:              code,
		Adapter:           AdapterEpay,
		DisplayName:       "Fake",
		CredentialsSealed: sealed,
		BaseURL:           "https://checkout.example.test",
		Methods:           []string{"alipay"},
		Enabled:           true,
		AcceptingNew:      true,
		MinAmount:         decimal.Zero,
	}
	if err := e.svc.repo.InsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	rec, err := e.svc.loadProvider(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// mkOrder 插一张待付的充值单（9.90 元）并登记给 stubSettler，返回 ID 与订单号：
// 意图、收款、挂账的 order_id 都有指向 orders 的外键。订单号满足 ^[A-Z0-9-]{1,64}$，随机取，全局唯一。
func (e *env) mkOrder(t *testing.T, ctx context.Context, user uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	orderNo := fmt.Sprintf("T%012d", n.Int64())
	var orderID uuid.UUID
	if err := e.row(ctx, `INSERT INTO orders (order_no, user_id, kind, subtotal_amount, total_amount, payable_amount, expires_at)
		VALUES (?, ?, 'topup', 9.90, 9.90, 9.90, now() + interval '30 minutes') RETURNING id`, orderNo, user).Scan(&orderID); err != nil {
		t.Fatalf("建订单: %v", err)
	}
	e.orders.add(orderID)
	e.settler.mu.Lock()
	e.settler.users[orderID] = user
	e.settler.mu.Unlock()
	return orderID, orderNo
}

// CreateIntent 是测试用的一步建单：BeginIntent（自己的事务）+ CompleteIntent。产品代码走 order.Pay 的两段式。
func (s *Service) CreateIntent(ctx context.Context, in CreateIntentInput) (IntentResult, error) {
	var d IntentDraft
	if err := s.tx.Do(ctx, func(ctx context.Context) error {
		var err error
		d, err = s.BeginIntent(ctx, in)
		return err
	}); err != nil {
		return IntentResult{}, err
	}
	return s.CompleteIntent(ctx, d)
}

func (e *env) createIntent(t *testing.T, ctx context.Context, providerCode string, orderID uuid.UUID, orderNo string, user uuid.UUID, amount string) IntentResult {
	t.Helper()
	amt := decimal.RequireFromString(amount)
	res, err := e.svc.CreateIntent(ctx, CreateIntentInput{
		OrderID:      orderID,
		OrderNo:      orderNo,
		UserID:       user,
		Amount:       amt,
		ProviderCode: providerCode,
		Title:        "测试订单",
		ClientIP:     "203.0.113.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// fakeInbound 构造带 HMAC 签名的替身渠道 JSON 回调（不报手续费）。
func fakeInbound(eventID, eventType, paymentRef, orderNo string, cents int64) provider.Inbound {
	return fakeInboundFee(eventID, eventType, paymentRef, orderNo, cents, 0)
}

// fakeInboundFee 同 fakeInbound，另带渠道手续费 feeCents（分），手续费一并签进签名。
func fakeInboundFee(eventID, eventType, paymentRef, orderNo string, cents, feeCents int64) provider.Inbound {
	body, _ := json.Marshal(map[string]any{
		"event_id":   eventID,
		"event_type": eventType,
		"payment_id": paymentRef,
		"order_no":   orderNo,
		"amount":     cents,
		"fee":        feeCents,
		"method":     "alipay",
	})
	h := http.Header{}
	h.Set("X-Aegis-Signature", fakeSign([]byte(fakeSecret), eventID, eventType, paymentRef, orderNo, cents, feeCents))
	return provider.Inbound{Method: http.MethodPost, Header: h, Body: body}
}

func unsignedInbound(eventID, eventType, paymentRef, orderNo string, cents int64) provider.Inbound {
	in := fakeInbound(eventID, eventType, paymentRef, orderNo, cents)
	in.Header.Set("X-Aegis-Signature", strings.Repeat("0", 64))
	return in
}

func (e *env) count(t *testing.T, ctx context.Context, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) ledgerTxnCount(t *testing.T, ctx context.Context, kind ledgersvc.Kind) int64 {
	t.Helper()
	return e.count(t, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = ?`, string(kind))
}

func (e *env) assertNoDrift(t *testing.T, ctx context.Context) {
	t.Helper()
	var agg sql.NullString
	err := e.row(ctx, `
SELECT string_agg(d.id, ',') FROM (
  SELECT a.id::text AS id
  FROM ledger_accounts a
  LEFT JOIN ledger_entries e ON e.account_id = a.id
  GROUP BY a.id, a.account_type, a.balance
  HAVING a.balance <> (
    CASE WHEN a.account_type IN ('channel_cash', 'platform_fee_expense', 'platform_promo_expense', 'suspense') THEN
      COALESCE(SUM(CASE WHEN e.direction = 'debit' THEN e.amount ELSE 0 END), 0)
      - COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE 0 END), 0)
    ELSE
      COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE 0 END), 0)
      - COALESCE(SUM(CASE WHEN e.direction = 'debit' THEN e.amount ELSE 0 END), 0)
    END
  )
) d`).Scan(&agg)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if agg.Valid && strings.TrimSpace(agg.String) != "" {
		t.Fatalf("科目余额缓存与分录合计不一致: %s", agg.String)
	}
}

func (e *env) accountBalance(t *testing.T, ctx context.Context, ref ledgersvc.AccountRef) decimal.Decimal {
	t.Helper()
	var owner *uuid.UUID
	if ref.Type.UserOwned() {
		uid := ref.UserID
		owner = &uid
	}
	var bal sql.NullString
	err := e.row(ctx, `
SELECT balance FROM ledger_accounts
WHERE account_type = ?
  AND owner_user_id IS NOT DISTINCT FROM ? AND owner_ref = ?`,
		string(ref.Type), owner, ref.Ref,
	).Scan(&bal)
	if err == sql.ErrNoRows {
		return decimal.Zero
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bal.Valid {
		return decimal.Zero
	}
	return dec(bal.String)
}

func dec(s string) decimal.Decimal {
	return decimal.RequireFromString(s)
}

func ackAlreadyHandled(body []byte) bool {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	v, ok := m["already_handled"]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
