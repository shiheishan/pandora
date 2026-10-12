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
	"github.com/aegispanel/aegis/internal/module/payment/provider/demo"
	paymentrepo "github.com/aegispanel/aegis/internal/module/payment/repo"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const demoSecret = "payment-pg-test-secret"

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
type stubSettler struct {
	mu         sync.Mutex
	users      map[uuid.UUID]uuid.UUID
	notPayable map[uuid.UUID]error
	calls      atomic.Int64
	ledger     *ledgersvc.Service
}

func (s *stubSettler) SettlePayment(ctx context.Context, in SettleInput) error {
	s.calls.Add(1)
	s.mu.Lock()
	if err, ok := s.notPayable[in.OrderID]; ok {
		s.mu.Unlock()
		return err
	}
	userID := s.users[in.OrderID]
	s.mu.Unlock()
	if userID == uuid.Nil {
		return fmt.Errorf("payment test: 未登记订单 %s", in.OrderID)
	}
	_, err := s.ledger.PostOrderPaid(ctx, ledgersvc.OrderPaid{
		OrderID:  in.OrderID,
		UserID:   userID,
		Currency: in.Currency,
		Total:    in.Amount,
		Paid:     in.Amount,
		Fee:      in.Fee,
		Provider: in.ProviderCode,
	})
	return err
}

type env struct {
	svc     *Service
	row     func(ctx context.Context, q string, args ...any) *sql.Row
	exec    func(ctx context.Context, q string, args ...any) error
	settler *stubSettler
	ledger  *ledgersvc.Service
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
	if commit {
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
	settings := settingssvc.New(settingsrepo.New(d), tx.NewManager(d), sealer, rdb, auditSvc)
	ledgerSvc := ledgersvc.New(ledgerrepo.New(d), tx.NewManager(d), settings, auditSvc)
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
		svc:     svc,
		settler: settler,
		ledger:  ledgerSvc,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
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

func (e *env) mkDemoProvider(t *testing.T, ctx context.Context, code string) providerRecord {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	sealed, err := e.svc.sealCredentials(id, credentials{Key: demoSecret})
	if err != nil {
		t.Fatal(err)
	}
	p := &model.Provider{
		ID:                id,
		Code:              code,
		Adapter:           AdapterDemo,
		DisplayName:       "Demo",
		CredentialsSealed: sealed,
		Currencies:        []string{"CNY"},
		BaseURL:           "https://checkout.example.test",
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

func (e *env) mkOrder(t *testing.T, ctx context.Context, user uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	orderID := uuid.Must(uuid.NewV7())
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	orderNo := fmt.Sprintf("T%012d", n.Int64())
	e.settler.mu.Lock()
	e.settler.users[orderID] = user
	e.settler.mu.Unlock()
	return orderID, orderNo
}

func (e *env) createIntent(t *testing.T, ctx context.Context, providerCode string, orderID uuid.UUID, orderNo string, user uuid.UUID, amount string) IntentResult {
	t.Helper()
	amt := decimal.RequireFromString(amount)
	res, err := e.svc.CreateIntent(ctx, CreateIntentInput{
		OrderID:      orderID,
		OrderNo:      orderNo,
		UserID:       user,
		Amount:       amt,
		Currency:     money.CNY,
		ProviderCode: providerCode,
		Title:        "测试订单",
		ClientIP:     "203.0.113.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func demoInbound(eventID, eventType, paymentRef, orderNo string, cents int64) provider.Inbound {
	body, _ := json.Marshal(map[string]any{
		"event_id":   eventID,
		"event_type": eventType,
		"payment_id": paymentRef,
		"order_no":   orderNo,
		"amount":     cents,
		"currency":   "CNY",
		"fee":        0,
		"method":     "alipay",
	})
	h := http.Header{}
	h.Set("X-Aegis-Signature", demo.Sign([]byte(demoSecret), eventID, orderNo, cents, "CNY"))
	return provider.Inbound{Method: http.MethodPost, Header: h, Body: body}
}

func unsignedInbound(eventID, eventType, paymentRef, orderNo string, cents int64) provider.Inbound {
	in := demoInbound(eventID, eventType, paymentRef, orderNo, cents)
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

func (e *env) accountBalance(t *testing.T, ctx context.Context, ref ledgersvc.AccountRef, cur money.Currency) decimal.Decimal {
	t.Helper()
	var owner *uuid.UUID
	if ref.Type.UserOwned() {
		uid := ref.UserID
		owner = &uid
	}
	var bal sql.NullString
	err := e.row(ctx, `
SELECT balance FROM ledger_accounts
WHERE account_type = ? AND currency = ?
  AND owner_user_id IS NOT DISTINCT FROM ? AND owner_ref = ?`,
		string(ref.Type), string(cur), owner, ref.Ref,
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
