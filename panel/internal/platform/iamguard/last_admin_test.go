package iamguard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type boolRow struct {
	value bool
	err   error
	text  string
}

func (r boolRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	switch out := dest[0].(type) {
	case *bool:
		*out = r.value
	case *string:
		*out = r.text
	default:
		return errors.New("unexpected scan target")
	}
	return nil
}

type guardTx struct {
	pgx.Tx
	execSQL  string
	querySQL string
	args     []any
	ok       bool
}

func (tx *guardTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.execSQL, tx.args = sql, args
	return pgconn.NewCommandTag("SELECT 1"), nil
}

func (tx *guardTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	tx.querySQL, tx.args = sql, args
	if strings.Contains(sql, "FROM tenants") {
		return boolRow{text: "11111111-1111-1111-1111-111111111111"}
	}
	return boolRow{value: tx.ok}
}

func TestLockLastAdministratorIsTenantScopedTransactionLock(t *testing.T) {
	tx := &guardTx{}
	if err := LockLastAdministrator(context.Background(), tx,
		"11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SELECT id::text FROM tenants", "FOR UPDATE"} {
		if !strings.Contains(tx.querySQL, want) {
			t.Fatalf("lock SQL missing %q: %s", want, tx.querySQL)
		}
	}
	if len(tx.args) != 1 {
		t.Fatalf("lock args=%#v", tx.args)
	}
}

func TestRequireEffectiveAdministratorFailsClosed(t *testing.T) {
	tx := &guardTx{ok: false}
	err := RequireEffectiveAdministrator(context.Background(), tx,
		"11111111-1111-1111-1111-111111111111")
	if !errors.Is(err, ErrLastEffectiveAdministrator) {
		t.Fatalf("err=%v, want last-admin conflict", err)
	}
	for _, want := range []string{"u.status = 'active'", "JOIN roles r", "scope_type = 'tenant'",
		"scope_id IS NULL", "iam.user.write", "iam.role.write", "expires_at IS NULL"} {
		if !strings.Contains(tx.querySQL, want) {
			t.Fatalf("invariant SQL missing %q: %s", want, tx.querySQL)
		}
	}

	tx.ok = true
	if err := RequireEffectiveAdministrator(context.Background(), tx,
		"11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatal(err)
	}
}

func TestRequireUserEffectiveAdministratorBindsTargetAndFailsClosed(t *testing.T) {
	tx := &guardTx{ok: false}
	err := RequireUserEffectiveAdministrator(context.Background(), tx,
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222")
	if !errors.Is(err, ErrNotEffectiveAdministrator) {
		t.Fatalf("err=%v, want target-admin refusal", err)
	}
	for _, want := range []string{"u.id = $2::uuid", "u.status = 'active'", "scope_type = 'tenant'",
		"scope_id IS NULL", "iam.user.write", "iam.role.write", "expires_at IS NULL"} {
		if !strings.Contains(tx.querySQL, want) {
			t.Fatalf("target invariant SQL missing %q: %s", want, tx.querySQL)
		}
	}
	if len(tx.args) != 2 {
		t.Fatalf("target invariant args=%#v", tx.args)
	}
	tx.ok = true
	if err := RequireUserEffectiveAdministrator(context.Background(), tx,
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222"); err != nil {
		t.Fatal(err)
	}
}
