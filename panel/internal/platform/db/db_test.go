package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeQueryRower struct {
	row pgx.Row
}

func TestIsForeignKeyViolation(t *testing.T) {
	if !IsForeignKeyViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("23503 not recognized")
	}
	if IsForeignKeyViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("unique violation misclassified")
	}
}

func (f fakeQueryRower) QueryRow(context.Context, string, ...any) pgx.Row { return f.row }

type fakeRow struct {
	currentRole       string
	sessionRole       string
	currentSuperuser  bool
	currentBypassRLS  bool
	sessionSuperuser  bool
	sessionBypassRLS  bool
	rowSecurity       string
	databaseTemporary bool
	publicCreate      bool
	appCreate         bool
	searchPath        string
	err               error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.currentRole
	*(dest[1].(*string)) = r.sessionRole
	*(dest[2].(*bool)) = r.currentSuperuser
	*(dest[3].(*bool)) = r.currentBypassRLS
	*(dest[4].(*bool)) = r.sessionSuperuser
	*(dest[5].(*bool)) = r.sessionBypassRLS
	*(dest[6].(*string)) = r.rowSecurity
	*(dest[7].(*bool)) = r.databaseTemporary
	*(dest[8].(*bool)) = r.publicCreate
	*(dest[9].(*bool)) = r.appCreate
	*(dest[10].(*string)) = r.searchPath
	return nil
}

func TestValidateRuntimeRole(t *testing.T) {
	tests := []struct {
		name    string
		row     fakeRow
		wantErr string
	}{
		{name: "safe application role", row: safeRuntimeRoleRow()},
		{name: "role switch", row: withRuntimeRoleMutation(func(row *fakeRow) { row.sessionRole = "operator" }), wantErr: "role switch"},
		{name: "current superuser", row: withRuntimeRoleMutation(func(row *fakeRow) {
			row.currentRole = "postgres"
			row.sessionRole = "postgres"
			row.currentSuperuser = true
		}), wantErr: "superuser"},
		{name: "session superuser", row: withRuntimeRoleMutation(func(row *fakeRow) { row.sessionSuperuser = true }), wantErr: "superuser"},
		{name: "current bypass rls", row: withRuntimeRoleMutation(func(row *fakeRow) {
			row.currentRole = "operator"
			row.sessionRole = "operator"
			row.currentBypassRLS = true
		}), wantErr: "BYPASSRLS"},
		{name: "session bypass rls", row: withRuntimeRoleMutation(func(row *fakeRow) { row.sessionBypassRLS = true }), wantErr: "BYPASSRLS"},
		{name: "row security off", row: withRuntimeRoleMutation(func(row *fakeRow) { row.rowSecurity = "off" }), wantErr: "row_security"},
		{name: "database temporary", row: withRuntimeRoleMutation(func(row *fakeRow) { row.databaseTemporary = true }), wantErr: "TEMPORARY"},
		{name: "public create", row: withRuntimeRoleMutation(func(row *fakeRow) { row.publicCreate = true }), wantErr: "schema public"},
		{name: "app create", row: withRuntimeRoleMutation(func(row *fakeRow) { row.appCreate = true }), wantErr: "schema app"},
		{name: "dsn search path override", row: withRuntimeRoleMutation(func(row *fakeRow) { row.searchPath = "pg_temp, public" }), wantErr: "search_path"},
		{name: "role lookup failure", row: fakeRow{err: errors.New("lookup failed")}, wantErr: "cannot verify"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRuntimeRole(context.Background(), fakeQueryRower{row: tt.row})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRuntimeRole() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRuntimeRole() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func safeRuntimeRoleRow() fakeRow {
	return fakeRow{
		currentRole: "aegis_app", sessionRole: "aegis_app",
		rowSecurity: "on", searchPath: "pg_catalog, public, pg_temp",
	}
}

func withRuntimeRoleMutation(mutate func(*fakeRow)) fakeRow {
	row := safeRuntimeRoleRow()
	mutate(&row)
	return row
}

func TestRuntimeRoleSecurityQueryAvoidsReservedAliases(t *testing.T) {
	for _, reservedAlias := range []string{"AS current_role", "AS session_role"} {
		if strings.Contains(runtimeRoleSecurityQuery, reservedAlias) {
			t.Fatalf("runtime role query uses reserved alias %q", reservedAlias)
		}
	}
}

func TestRuntimeRoleSecurityQueryUsesPostgresBypassRLSColumn(t *testing.T) {
	if !strings.Contains(runtimeRoleSecurityQuery, ".rolbypassrls") {
		t.Fatal("runtime role query must use PostgreSQL pg_roles.rolbypassrls")
	}
	if strings.Contains(runtimeRoleSecurityQuery, ".rolbypassrl,") {
		t.Fatal("runtime role query contains misspelled pg_roles.rolbypassrl")
	}
}

func TestRuntimeRoleSecurityQueryCoversSearchPathPrivileges(t *testing.T) {
	for _, required := range []string{
		"has_database_privilege(current_user, current_database(), 'TEMPORARY')",
		"has_schema_privilege(current_user, 'public', 'CREATE')",
		"has_schema_privilege(current_user, 'app', 'CREATE')",
		"current_setting('search_path')",
	} {
		if !strings.Contains(runtimeRoleSecurityQuery, required) {
			t.Fatalf("runtime role query is missing %q", required)
		}
	}
}

type cleanupRollbackProbe struct {
	called      bool
	ctxErr      error
	hasDeadline bool
	remaining   time.Duration
}

func (p *cleanupRollbackProbe) Rollback(ctx context.Context) error {
	p.called = true
	p.ctxErr = ctx.Err()
	deadline, ok := ctx.Deadline()
	p.hasDeadline = ok
	if ok {
		p.remaining = time.Until(deadline)
	}
	return errors.New("probe rollback")
}

func TestRollbackForCleanupUsesIndependentBoundedContext(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if requestCtx.Err() == nil {
		t.Fatal("request context fixture was not canceled")
	}
	probe := &cleanupRollbackProbe{}
	rollbackForCleanup(probe)
	if !probe.called || probe.ctxErr != nil || !probe.hasDeadline {
		t.Fatalf("cleanup rollback called=%t ctxErr=%v deadline=%t", probe.called, probe.ctxErr, probe.hasDeadline)
	}
	if probe.remaining <= 0 || probe.remaining > 2*time.Second {
		t.Fatalf("cleanup rollback deadline remaining=%s", probe.remaining)
	}
}
