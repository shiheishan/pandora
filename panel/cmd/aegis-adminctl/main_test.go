// [INPUT]: 依赖 upsertTenantRoleBinding、replacePasswordAndRevokeCredentials、readPasswordLine，依赖 platform/sourcetest 取本包全部源码
// [OUTPUT]: 对外提供 TestUpsertTenantRoleBindingHandlesNullScopeWithoutOnConflict、TestReplacePasswordRevokesAllCredentialClasses、TestReplacePasswordFailsBeforeRevocationWhenPasswordRowIsAmbiguous、TestReadPasswordLinePreservesSpacesAndTrimsOnlyLineEnding、TestAdministratorPasswordCommandsRejectPasswordArguments
// [POS]: cmd/aegis-adminctl 的单元与源码契约：角色绑定不用 ON CONFLICT、改密与吊销凭据的 SQL 次序、口令只经标准输入
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

type bindingTx struct {
	pgx.Tx
	updateRows int64
	statements []string
}

func (tx *bindingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.statements = append(tx.statements, sql)
	if strings.Contains(sql, "UPDATE role_bindings") {
		return pgconn.NewCommandTag("UPDATE " + string(rune('0'+tx.updateRows))), nil
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func TestUpsertTenantRoleBindingHandlesNullScopeWithoutOnConflict(t *testing.T) {
	tx := &bindingTx{}
	if err := upsertTenantRoleBinding(context.Background(), tx, "tenant", "user", "role", "reason", nil); err != nil {
		t.Fatal(err)
	}
	if len(tx.statements) != 2 || !strings.Contains(tx.statements[0], "scope_id IS NULL") ||
		!strings.Contains(tx.statements[1], "scope_id, grant_reason") {
		t.Fatalf("unexpected insert path SQL: %#v", tx.statements)
	}
	for _, sql := range tx.statements {
		if strings.Contains(sql, "ON CONFLICT") {
			t.Fatal("legacy NULL-distinct constraint must not be used for tenant binding upsert")
		}
	}

	tx = &bindingTx{updateRows: 2}
	if err := upsertTenantRoleBinding(context.Background(), tx, "tenant", "user", "role", "reason", nil); err != nil {
		t.Fatal(err)
	}
	if len(tx.statements) != 1 {
		t.Fatalf("existing binding should update without insert: %#v", tx.statements)
	}
}

type credentialTx struct {
	pgx.Tx
	passwordRows int64
	sessionRows  int64
	refreshRows  int64
	statements   []string
}

func (tx *credentialTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	tx.statements = append(tx.statements, sql)
	return credentialRow{}
}

type credentialRow struct{}

func (credentialRow) Scan(dest ...any) error {
	if len(dest) != 2 {
		return errors.New("unexpected credential capability scan")
	}
	*(dest[0].(*bool)) = false
	*(dest[1].(*bool)) = false
	return nil
}

func (tx *credentialTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.statements = append(tx.statements, sql)
	switch {
	case strings.Contains(sql, "INSERT INTO user_passwords"):
		return pgconn.NewCommandTag("INSERT 0 " + string(rune('0'+tx.passwordRows))), nil
	case strings.Contains(sql, "UPDATE sessions"):
		return pgconn.NewCommandTag("UPDATE " + string(rune('0'+tx.sessionRows))), nil
	case strings.Contains(sql, "UPDATE refresh_tokens"):
		return pgconn.NewCommandTag("UPDATE " + string(rune('0'+tx.refreshRows))), nil
	default:
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}
}

func TestReplacePasswordRevokesAllCredentialClasses(t *testing.T) {
	tx := &credentialTx{passwordRows: 1, sessionRows: 3, refreshRows: 2}
	sessions, refresh, families, err := replacePasswordAndRevokeCredentials(
		context.Background(), tx, "tenant", "user", "$argon2id$test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 3 || refresh != 2 || families != 0 {
		t.Fatalf("revoked sessions/refresh/families=%d/%d/%d, want 3/2/0", sessions, refresh, families)
	}
	if len(tx.statements) != 4 ||
		!strings.Contains(tx.statements[0], "INSERT INTO user_passwords") ||
		!strings.Contains(tx.statements[1], "revoked_reason = 'password_changed'") ||
		!strings.Contains(tx.statements[2], "status = 'revoked'") ||
		!strings.Contains(tx.statements[3], "to_regclass") {
		t.Fatalf("unexpected credential rotation SQL order: %#v", tx.statements)
	}
}

func TestReplacePasswordFailsBeforeRevocationWhenPasswordRowIsAmbiguous(t *testing.T) {
	tx := &credentialTx{passwordRows: 0, sessionRows: 3, refreshRows: 2}
	if _, _, _, err := replacePasswordAndRevokeCredentials(
		context.Background(), tx, "tenant", "user", "$argon2id$test",
	); err == nil {
		t.Fatal("zero-row password rotation must fail closed")
	}
	if len(tx.statements) != 1 {
		t.Fatalf("credential revocation must not run after ambiguous password write: %#v", tx.statements)
	}
}

func TestReadPasswordLinePreservesSpacesAndTrimsOnlyLineEnding(t *testing.T) {
	got, err := readPasswordLine(strings.NewReader("  Abcdef12  \r\nignored"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "  Abcdef12  " {
		t.Fatalf("password=%q, want spaces preserved", got)
	}
	if _, err := readPasswordLine(strings.NewReader("")); err == nil {
		t.Fatal("empty stdin must fail")
	}
}

func TestAdministratorPasswordCommandsRejectPasswordArguments(t *testing.T) {
	text := sourcetest.Load(t, ".").Source()
	if strings.Contains(text, `fs.String("password"`) || strings.Contains(text, `--password 或`) {
		t.Fatal("administrator passwords must never be accepted through process arguments")
	}
	if strings.Count(text, `fs.Bool("password-stdin"`) != 2 {
		t.Fatal("create and reset-password must both require stdin-only passwords")
	}
}
