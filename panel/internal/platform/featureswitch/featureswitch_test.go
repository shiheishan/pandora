package featureswitch

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

type fakeQuerier struct {
	enabled bool
	err     error
	scope   db.Scope
	args    []any
}

func (q *fakeQuerier) QueryRowScoped(_ context.Context, s db.Scope, _ string, args []any, dest ...any) error {
	q.scope, q.args = s, args
	if q.err != nil {
		return q.err
	}
	*(dest[0].(*bool)) = q.enabled
	return nil
}

// 缺行视为开启（急停开关 fail open）；读到的值原样返回；库错误不吞
func TestEnabledFailsOpenOnlyOnMissingRow(t *testing.T) {
	ctx := context.Background()
	q := &fakeQuerier{enabled: false}
	if on, err := Enabled(ctx, q, "t", "billing.checkout"); err != nil || on {
		t.Fatalf("closed switch: on=%v err=%v", on, err)
	}
	if q.scope != (db.Scope{TenantID: "t"}) || len(q.args) != 2 || q.args[0] != "t" || q.args[1] != "billing.checkout" {
		t.Fatalf("scope=%+v args=%v", q.scope, q.args)
	}
	if on, err := Enabled(ctx, &fakeQuerier{err: pgx.ErrNoRows}, "t", "x"); err != nil || !on {
		t.Fatalf("missing row: on=%v err=%v, want open", on, err)
	}
	boom := errors.New("down")
	if _, err := Enabled(ctx, &fakeQuerier{err: boom}, "t", "x"); !errors.Is(err, boom) {
		t.Fatalf("database error = %v", err)
	}
}
