package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/ledger/repo"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type env struct {
	svc  *Service
	row  func(ctx context.Context, q string, args ...any) *sql.Row
	exec func(ctx context.Context, q string, args ...any) error
	// replica 只在真提交的环境里有：在一个 session_replication_role = replica 的事务里执行 q，
	// 用来删掉本用例建的订单（orders 有禁删触发器）。回滚事务的环境为 nil。
	replica func(q string, args ...any) error
}

func newEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	_, rdb := testkit.Redis(t)

	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
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
	svc := New(repo.New(d), tx.NewManager(d), settings, auditSvc)

	ctx := testkit.TxCtx(t, d)
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.7", UserAgent: "ledger-test/1.0"})

	e := &env{
		svc: svc,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	return e, ctx
}

func newCommitEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	// 真提交的用例开头清空本模块四张表，结束时再清一次，不把已提交的账留给后面在回滚事务里跑的用例
	ledgerTables := []string{"balance_holds", "ledger_entries", "ledger_transactions", "ledger_accounts"}
	testkit.Truncate(t, d, ledgerTables...)
	t.Cleanup(func() { testkit.Truncate(t, d, ledgerTables...) })
	_, rdb := testkit.Redis(t)

	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x02
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
	svc := New(repo.New(d), tx.NewManager(d), settings, auditSvc)

	e := &env{
		svc: svc,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
		replica: func(q string, args ...any) error {
			g := d.Base().Begin()
			if g.Error != nil {
				return g.Error
			}
			defer g.Rollback()
			if err := g.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
				return err
			}
			if err := g.Exec(q, args...).Error; err != nil {
				return err
			}
			return g.Commit().Error
		},
	}
	ctx := context.Background()
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.7", UserAgent: "ledger-test/1.0"})
	return e, ctx
}

func (e *env) mkUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	email := fmt.Sprintf("ledger-%s@example.test", uuid.New().String())
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// newOrder 插一张待付的新购订单并返回 ID：balance_holds.order_id 有指向 orders 的外键，冻结要挂在真订单上。
// 订单号用随机串，满足 ^[A-Z0-9-]{1,64}$ 且全局唯一。真提交的环境在用例结束时连同它的冻结一起删掉。
func (e *env) newOrder(t *testing.T, ctx context.Context, user uuid.UUID, cur money.Currency) uuid.UUID {
	t.Helper()
	no := "T-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO orders (order_no, user_id, kind, currency, subtotal_amount, total_amount, payable_amount, expires_at)
		VALUES (?, ?, 'new', ?, 10, 10, 10, now() + interval '30 minutes') RETURNING id`, no, user, string(cur)).Scan(&id); err != nil {
		t.Fatalf("建订单: %v", err)
	}
	if e.replica != nil {
		// 先删引用订单的冻结，再删订单
		t.Cleanup(func() {
			if err := e.replica(`DELETE FROM balance_holds WHERE order_id = ?`, id); err != nil {
				t.Errorf("清冻结: %v", err)
			}
			if err := e.replica(`DELETE FROM orders WHERE id = ?`, id); err != nil {
				t.Errorf("清订单: %v", err)
			}
		})
	}
	return id
}

func (e *env) fund(t *testing.T, ctx context.Context, user uuid.UUID, cur money.Currency, amount string) {
	t.Helper()
	amt := dec(amount)
	_, err := e.svc.Post(ctx, Posting{
		Kind:     KindBalanceTopup,
		Currency: cur,
		Legs: []Leg{
			{Account: PlatformAccount(ChannelCash, "test"), Direction: Debit, Amount: amt},
			{Account: UserAccount(UserBalance, user), Direction: Credit, Amount: amt},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) balanceOf(t *testing.T, ctx context.Context, user uuid.UUID, cur money.Currency) decimal.Decimal {
	t.Helper()
	bal, err := e.svc.Balance(ctx, user, cur)
	if err != nil {
		t.Fatal(err)
	}
	return bal
}

func (e *env) accountBalance(t *testing.T, ctx context.Context, ref AccountRef, cur money.Currency) decimal.Decimal {
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

func asAdmin(ctx context.Context, id uuid.UUID) context.Context {
	return authn.With(ctx, authn.Principal{
		UserID:    id,
		SessionID: "s-admin",
		Audience:  authn.AudienceAdmin,
	})
}

func asUser(ctx context.Context, id uuid.UUID) context.Context {
	return authn.With(ctx, authn.Principal{
		UserID:    id,
		SessionID: "s-user",
		Audience:  authn.AudiencePublic,
	})
}

func codeOf(err error) errs.Code {
	if err == nil {
		return ""
	}
	return errs.As(err).Code
}

func dec(s string) decimal.Decimal {
	return decimal.RequireFromString(s)
}
