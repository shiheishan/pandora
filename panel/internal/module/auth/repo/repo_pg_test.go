package repo_test

import (
	"context"
	"testing"

	"github.com/aegispanel/aegis/internal/module/auth/repo"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

func newRepoPG(t *testing.T) (*repo.Repo, *db.DB, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	return repo.New(d), d, testkit.TxCtx(t, d)
}

// addUser 在回滚事务里建一个用户（只填必填列）。
func addUser(t *testing.T, d *db.DB, ctx context.Context, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := d.Conn(ctx).Raw("INSERT INTO users (email) VALUES (?) RETURNING id", email).Scan(&id).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func TestAdminsCRUD(t *testing.T) {
	r, d, ctx := newRepoPG(t)
	a := addUser(t, d, ctx, "a@example.com")
	b := addUser(t, d, ctx, "b@example.com")

	if ok, err := r.IsAdmin(ctx, a); err != nil || ok {
		t.Fatalf("IsAdmin 空名单=%v err=%v", ok, err)
	}
	if ok, err := r.InsertAdmin(ctx, a, nil); err != nil || !ok {
		t.Fatalf("InsertAdmin=%v err=%v", ok, err)
	}
	// 已在名单里：不报错、返回 false
	if ok, err := r.InsertAdmin(ctx, a, &b); err != nil || ok {
		t.Fatalf("重复 InsertAdmin=%v err=%v", ok, err)
	}
	if ok, err := r.InsertAdmin(ctx, b, &a); err != nil || !ok {
		t.Fatalf("InsertAdmin b=%v err=%v", ok, err)
	}
	if ok, err := r.IsAdmin(ctx, a); err != nil || !ok {
		t.Fatalf("IsAdmin=%v err=%v", ok, err)
	}
	rows, err := r.Admins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].CreatedAt.IsZero() {
		t.Fatalf("Admins=%+v", rows)
	}

	if ok, err := r.DeleteAdmin(ctx, a); err != nil || !ok {
		t.Fatalf("DeleteAdmin=%v err=%v", ok, err)
	}
	if ok, err := r.DeleteAdmin(ctx, a); err != nil || ok {
		t.Fatalf("重复 DeleteAdmin=%v err=%v", ok, err)
	}
	if ok, err := r.IsAdmin(ctx, a); err != nil || ok {
		t.Fatalf("删除后 IsAdmin=%v err=%v", ok, err)
	}
}

func TestAdminsForeignKeys(t *testing.T) {
	r, d, ctx := newRepoPG(t)
	a := addUser(t, d, ctx, "fk-a@example.com")
	b := addUser(t, d, ctx, "fk-b@example.com")
	if _, err := r.InsertAdmin(ctx, b, &a); err != nil {
		t.Fatal(err)
	}
	// 授予人被删：granted_by 置空，名单保留
	if err := d.Conn(ctx).Exec("DELETE FROM users WHERE id = ?", a).Error; err != nil {
		t.Fatal(err)
	}
	var grantedBy *uuid.UUID
	if err := d.Conn(ctx).Raw("SELECT granted_by FROM admins WHERE user_id = ?", b).Scan(&grantedBy).Error; err != nil {
		t.Fatal(err)
	}
	if grantedBy != nil {
		t.Fatalf("granted_by=%v want NULL", grantedBy)
	}
	// 管理员账号被删：名单行跟着删
	if err := d.Conn(ctx).Exec("DELETE FROM users WHERE id = ?", b).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := r.IsAdmin(ctx, b); err != nil || ok {
		t.Fatalf("账号删除后 IsAdmin=%v err=%v", ok, err)
	}
	// 不存在的用户不能进名单
	if _, err := r.InsertAdmin(ctx, uuid.New(), nil); err == nil {
		t.Fatal("外键应拒绝不存在的用户")
	}
}

func TestLockAdminSetInTx(t *testing.T) {
	r, _, ctx := newRepoPG(t)
	if err := r.LockAdminSet(ctx); err != nil {
		t.Fatal(err)
	}
}
