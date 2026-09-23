package credentialrevocation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeQueryRower struct {
	relationExists bool
	functionExists bool
	revoked        int64
	queries        []string
	args           [][]any
}

func (f *fakeQueryRower) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.queries = append(f.queries, sql)
	f.args = append(f.args, args)
	if strings.Contains(sql, "to_regclass") {
		return fakeRow{values: []any{f.relationExists, f.functionExists}}
	}
	return fakeRow{values: []any{f.revoked}}
}

type fakeRow struct {
	values []any
}

func (r fakeRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return errors.New("unexpected scan arity")
	}
	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *bool:
			*target = value.(bool)
		case *int64:
			*target = value.(int64)
		default:
			return errors.New("unexpected scan target")
		}
	}
	return nil
}

func TestRevokeRefreshFamiliesIsSchema40Compatible(t *testing.T) {
	fake := &fakeQueryRower{}
	revoked, err := RevokeRefreshFamilies(context.Background(), fake, "tenant", "user")
	if err != nil || revoked != 0 {
		t.Fatalf("revoked=%d err=%v, want zero success", revoked, err)
	}
	if len(fake.queries) != 1 {
		t.Fatalf("queries=%d, protected table must not be touched", len(fake.queries))
	}
}

func TestRevokeRefreshFamiliesFailsClosedForPartialCapability(t *testing.T) {
	for _, test := range []fakeQueryRower{
		{relationExists: true},
		{functionExists: true},
	} {
		fake := test
		_, err := RevokeRefreshFamilies(context.Background(), &fake, "tenant", "user")
		if !errors.Is(err, ErrRefreshFamilyRevocationUnavailable) {
			t.Fatalf("err=%v, want capability error", err)
		}
		if len(fake.queries) != 1 {
			t.Fatalf("queries=%d, unsafe capability must not be invoked", len(fake.queries))
		}
	}
}

func TestRevokeRefreshFamiliesUsesOnlyFixedFunction(t *testing.T) {
	fake := &fakeQueryRower{relationExists: true, functionExists: true, revoked: 4}
	revoked, err := RevokeRefreshFamilies(context.Background(), fake, "tenant-id", "user-id")
	if err != nil || revoked != 4 {
		t.Fatalf("revoked=%d err=%v, want 4", revoked, err)
	}
	if len(fake.queries) != 2 || !strings.Contains(fake.queries[1], "app.revoke_user_refresh_families") {
		t.Fatalf("unexpected queries: %#v", fake.queries)
	}
	if strings.Contains(fake.queries[1], "UPDATE refresh_families") {
		t.Fatal("runtime code must not update the protected base table")
	}
	if len(fake.args[1]) != 2 || fake.args[1][0] != "tenant-id" || fake.args[1][1] != "user-id" {
		t.Fatalf("unexpected function args: %#v", fake.args[1])
	}
}
