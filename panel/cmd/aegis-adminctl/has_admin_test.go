package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// presenceTx 只实现 QueryRow：返回预设的「有没有有效管理员」或错误。
type presenceTx struct {
	pgx.Tx
	present bool
	err     error
	sql     string
}

type presenceRow struct {
	present bool
	err     error
}

func (r presenceRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*bool) = r.present
	return nil
}

func (tx *presenceTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	tx.sql = sql
	return presenceRow{present: tx.present, err: tx.err}
}

func TestAdministratorPresentUsesTheLastAdministratorDefinition(t *testing.T) {
	ctx := context.Background()
	tx := &presenceTx{present: true}
	got, err := administratorPresent(ctx, tx, "tenant")
	if err != nil || !got {
		t.Fatalf("present: got %v, %v", got, err)
	}
	// 口径必须是守卫那一份：永久、租户级、同时能改用户与角色
	for _, fragment := range []string{"iam.user.write", "iam.role.write", "rb.expires_at IS NULL", "u.status = 'active'"} {
		if !strings.Contains(tx.sql, fragment) {
			t.Fatalf("has-admin query lost %q: %s", fragment, tx.sql)
		}
	}

	got, err = administratorPresent(ctx, &presenceTx{present: false}, "tenant")
	if err != nil || got {
		t.Fatalf("absent: got %v, %v", got, err)
	}

	boom := errors.New("connection reset")
	if _, err := administratorPresent(ctx, &presenceTx{err: boom}, "tenant"); !errors.Is(err, boom) {
		t.Fatalf("database errors must not be reported as no administrator: %v", err)
	}
}

func TestExitCodeSeparatesNoAdministratorFromFailures(t *testing.T) {
	if got := exitCode(&exitCodeError{code: exitNoAdministrator, msg: "none"}); got != 3 {
		t.Fatalf("no administrator exit code = %d, want 3", got)
	}
	if got := exitCode(errors.New("dial tcp: refused")); got != 1 {
		t.Fatalf("ordinary failure exit code = %d, want 1", got)
	}
}
