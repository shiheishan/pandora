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
	testkit.Truncate(t, d,
		"balance_holds",
		"ledger_entries",
		"ledger_transactions",
		"ledger_accounts",
	)
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
