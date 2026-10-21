package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PANDORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PANDORA_TEST_DATABASE_URL 未设置")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// downAll 回退到 0，只给往返测试用（二进制只提供逐个回退的 migrate down）。
func downAll(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	_, err = p.DownTo(ctx, 0)
	return err
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := Up(ctx, db); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := downAll(ctx, db); err != nil {
		t.Fatalf("down all: %v", err)
	}
	if err := Up(ctx, db); err != nil {
		t.Fatalf("up again: %v", err)
	}
	st, err := Status(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) == 0 {
		t.Fatal("no migrations found")
	}
	for _, v := range st {
		if !v.Applied {
			t.Fatalf("migration %d %s not applied", v.Version, v.Name)
		}
	}
}

func TestDenyMutation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := Up(ctx, db); err != nil {
		t.Fatalf("up: %v", err)
	}
	stmts := []string{
		`DROP TABLE IF EXISTS migrations_test_append`,
		`CREATE TABLE migrations_test_append (id int PRIMARY KEY)`,
		`CREATE TRIGGER t_append BEFORE UPDATE OR DELETE ON migrations_test_append FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation()`,
		`INSERT INTO migrations_test_append VALUES (1)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP TABLE IF EXISTS migrations_test_append`) })

	for _, s := range []string{`UPDATE migrations_test_append SET id = 2`, `DELETE FROM migrations_test_append`} {
		_, err := db.ExecContext(ctx, s)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23001" {
			t.Fatalf("%s: want restrict_violation, got %v", s, err)
		}
	}
}
