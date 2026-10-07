package iamguard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type authorityRow struct {
	staff, covered bool
	err            error
}

func (r authorityRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.staff
	*(dest[1].(*bool)) = r.covered
	return nil
}

type authorityTx struct {
	pgx.Tx
	row  authorityRow
	sql  string
	args []any
}

func (tx *authorityTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	tx.sql, tx.args = sql, args
	return tx.row
}

const (
	authTenant = "11111111-1111-1111-1111-111111111111"
	authActor  = "22222222-2222-2222-2222-222222222222"
	authTarget = "33333333-3333-3333-3333-333333333333"
)

func TestCanManageRefusesTargetsOutsideActorPermissions(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  authorityRow
		want error
	}{
		{"ordinary user", authorityRow{staff: false, covered: true}, nil},
		{"same or lower staff", authorityRow{staff: true, covered: true}, nil},
		{"outranking staff", authorityRow{staff: true, covered: false}, ErrTargetOutranksActor},
	} {
		tx := &authorityTx{row: tc.row}
		a, err := CanManage(context.Background(), tx, authTenant, authActor, authTarget)
		if !errors.Is(err, tc.want) || a.Staff != tc.row.staff || a.Covered != tc.row.covered {
			t.Errorf("%s: authority=%+v err=%v, want %v", tc.name, a, err, tc.want)
		}
		if len(tx.args) != 3 || tx.args[0] != authTenant || tx.args[1] != authActor || tx.args[2] != authTarget {
			t.Fatalf("%s: args=%#v, want tenant, actor, target", tc.name, tx.args)
		}
	}
	dbErr := errors.New("connection reset")
	if _, err := CanManage(context.Background(), &authorityTx{row: authorityRow{err: dbErr}},
		authTenant, authActor, authTarget); !errors.Is(err, dbErr) {
		t.Fatalf("database error not propagated: %v", err)
	}
	if ErrTargetOutranksActor.Code != httpx.CodeForbidden {
		t.Fatalf("outrank refusal code = %q, want forbidden (403)", ErrTargetOutranksActor.Code)
	}
}

// 口径钉住：目标算全部未过期绑定（不限范围），操作者只算租户范围未过期绑定；
// 用集合差判断「目标有而操作者没有」的权限。
func TestAuthoritySQLComparesPermissionSets(t *testing.T) {
	target, actor, ok := strings.Cut(authoritySQL, "EXCEPT")
	if !ok {
		t.Fatal("authority SQL must compare permission sets with EXCEPT")
	}
	for _, want := range []string{"NOT EXISTS", "rb.user_id = $3::uuid", "rb.expires_at > now()"} {
		if !strings.Contains(target, want) {
			t.Errorf("target side missing %q", want)
		}
	}
	if strings.Contains(target, "scope_type") {
		t.Error("target side must count bindings of every scope")
	}
	for _, want := range []string{"rb.user_id = $2::uuid", "rb.expires_at > now()",
		"rb.scope_type = 'tenant'", "rb.scope_id IS NULL", "JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id"} {
		if !strings.Contains(actor, want) {
			t.Errorf("actor side missing %q", want)
		}
	}
}

func TestIsStaffUsesSharedDefinition(t *testing.T) {
	staffTx := &staffTx{value: true}
	staff, err := IsStaff(context.Background(), staffTx, authTenant, authTarget)
	if err != nil || !staff {
		t.Fatalf("IsStaff = %v, %v", staff, err)
	}
	if !strings.Contains(staffTx.sql, "FROM role_bindings rb") || !strings.Contains(staffTx.sql, "rb.user_id = $2::uuid") ||
		strings.Contains(staffTx.sql, "expires_at") || strings.Contains(staffTx.sql, "scope_type") {
		t.Fatalf("staff definition drifted: %s", staffTx.sql)
	}
	if len(staffTx.args) != 2 || staffTx.args[0] != authTenant || staffTx.args[1] != authTarget {
		t.Fatalf("args=%#v", staffTx.args)
	}
	// authoritySQL 里的 Staff 列用同一段定义，只是目标参数位置是 $3
	if !strings.Contains(authoritySQL, staffBindingSQL("$3")) {
		t.Fatal("authority SQL must embed the shared staff definition")
	}
}

type staffTx struct {
	pgx.Tx
	value bool
	sql   string
	args  []any
}

func (tx *staffTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	tx.sql, tx.args = sql, args
	return boolRow{value: tx.value}
}
