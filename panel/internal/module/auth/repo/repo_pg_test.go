package repo_test

import (
	"context"
	"sort"
	"testing"

	"github.com/aegispanel/aegis/internal/module/auth/repo"
	"github.com/aegispanel/aegis/internal/module/auth/service"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

func newRepoPG(t *testing.T) (*repo.Repo, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	return repo.New(d), testkit.TxCtx(t, d)
}

func sortedStrings(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}

func TestPermissionSeedMatchesConstants(t *testing.T) {
	r, ctx := newRepoPG(t)
	fromDB, err := r.PermissionCodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := sortedStrings(service.AllPermissions())
	got := sortedStrings(fromDB)
	if len(got) != 44 {
		t.Fatalf("count %d want 44", len(got))
	}
	if len(want) != 44 {
		t.Fatalf("AllPermissions count %d want 44", len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] db=%q const=%q", i, got[i], want[i])
		}
	}
}

func TestRolesSeeded(t *testing.T) {
	r, ctx := newRepoPG(t)
	rows, err := r.Roles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("count %d want 6", len(rows))
	}
	expected := map[string]bool{
		service.RolePlatformAdmin:    true,
		service.RoleTenantAdmin:      true,
		service.RoleFinance:          true,
		service.RoleCustomerSupport:  true,
		service.RoleResourceOperator: true,
		service.RoleSecurityAuditor:  true,
	}
	for _, row := range rows {
		if !expected[row.Code] {
			t.Fatalf("unexpected role code %q", row.Code)
		}
		delete(expected, row.Code)
		if row.Code == service.RolePlatformAdmin {
			if row.Permissions != 44 {
				t.Fatalf("platform_admin permissions %d want 44", row.Permissions)
			}
		} else if row.Permissions <= 0 {
			t.Fatalf("role %q permissions %d want > 0", row.Code, row.Permissions)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing roles: %v", expected)
	}
}

func TestRoleByCode(t *testing.T) {
	r, ctx := newRepoPG(t)
	row, ok, err := r.RoleByCode(ctx, service.RoleFinance)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("finance: ok=false")
	}
	if row.Name == "" {
		t.Fatal("finance: empty name")
	}

	_, ok, err = r.RoleByCode(ctx, "nope")
	if err != nil {
		t.Fatalf("nope: err=%v want nil", err)
	}
	if ok {
		t.Fatal("nope: ok=true want false")
	}
}

func TestLockAdminSetInTx(t *testing.T) {
	r, ctx := newRepoPG(t)
	if err := r.LockAdminSet(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEveryRolePermissionIsKnown(t *testing.T) {
	d := testkit.DB(t)
	ctx := context.Background()

	var codes []string
	if err := d.Base().WithContext(ctx).Raw(
		"SELECT DISTINCT permission_code FROM role_permissions",
	).Scan(&codes).Error; err != nil {
		t.Fatal(err)
	}
	for _, code := range codes {
		if !service.IsPermission(code) {
			t.Fatalf("unknown permission in role_permissions: %q", code)
		}
	}
}

func TestStaffQueriesWithoutBindings(t *testing.T) {
	r, ctx := newRepoPG(t)
	a, b := uuid.New(), uuid.New()
	staff, err := r.HasAnyRole(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if staff {
		t.Fatal("HasAnyRole: 没有绑定却为 true")
	}
	// 目标没有任何生效权限：任何操作者都覆盖它
	ok, err := r.Covers(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Covers: 目标无权限时应为 true")
	}
}
