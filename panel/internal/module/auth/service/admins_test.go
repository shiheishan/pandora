package service

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func countAction(al *auditLog, action string) int {
	n := 0
	for _, r := range al.events {
		if r.ev.Action == action {
			n++
		}
	}
	return n
}

func TestGrantAdminRevokesPortal(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	target := h.Users.addUser("grant@example.com", "pass", UserStatusActive)

	tok, err := h.S.Login(ctx, authn.AudiencePublic, "grant@example.com", "pass")
	if err != nil {
		t.Fatalf("public login: %v", err)
	}

	// 命令行授予：没有登录主体，granted_by 为空
	if err := h.S.GrantAdmin(ctx, target); err != nil {
		t.Fatalf("GrantAdmin: %v", err)
	}
	if _, err := h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("授予后门户会话应失效: %v", err)
	}
	if len(h.Store.inserts) != 1 || h.Store.inserts[0] != target || h.Store.grantBy[0] != nil {
		t.Fatalf("inserts=%v grantBy=%v", h.Store.inserts, h.Store.grantBy)
	}
	if h.Store.locks != 1 {
		t.Fatalf("应取一次管理员集合锁，得 %d", h.Store.locks)
	}
	if n := countAction(h.Audit, ActionAdminGranted); n != 1 {
		t.Fatalf("授予审计=%d want 1", n)
	}

	// 幂等：再授予一次不报错、不写审计
	if err := h.S.GrantAdmin(ctx, target); err != nil {
		t.Fatalf("重复授予: %v", err)
	}
	if n := countAction(h.Audit, ActionAdminGranted); n != 1 {
		t.Fatalf("重复授予不应写审计，得 %d", n)
	}

	// 授予后登门户被拦，登后台成功
	if _, err := h.S.Login(ctx, authn.AudiencePublic, "grant@example.com", "pass"); !errors.Is(err, ErrStaffPortalLogin) {
		t.Fatalf("管理员登门户应 403: %v", err)
	}
	if _, err := h.S.Login(ctx, authn.AudienceAdmin, "grant@example.com", "pass"); err != nil {
		t.Fatalf("管理员登后台: %v", err)
	}

	if err := h.S.GrantAdmin(ctx, uuid.Must(uuid.NewV7())); errs.As(err).Code != errs.NotFound {
		t.Fatalf("授予不存在的用户应 404: %v", err)
	}
}

func TestRevokeAdmin(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	a := h.Users.addUser("a@example.com", "pass", UserStatusActive)
	b := h.Users.addUser("b@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(a)
	h.Store.addAdmin(b)

	tok := adminLogin(t, h, ctx, "b@example.com", "pass")

	if err := h.S.RevokeAdmin(ctx, b); err != nil {
		t.Fatalf("RevokeAdmin: %v", err)
	}
	// 后台会话当场失效，再登后台与口令错一样 401（IAM-006）
	if _, err := h.S.Authenticate(ctx, authn.AudienceAdmin, tok.AccessToken); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("取消后后台会话应失效: %v", err)
	}
	if _, err := h.S.Login(ctx, authn.AudienceAdmin, "b@example.com", "pass"); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("被取消的管理员登后台应 401: %v", err)
	}
	if n := countAction(h.Audit, ActionAdminRevoked); n != 1 {
		t.Fatalf("取消审计=%d want 1", n)
	}

	// 幂等：本来就不是管理员
	if err := h.S.RevokeAdmin(ctx, b); err != nil {
		t.Fatalf("重复取消: %v", err)
	}
	if n := countAction(h.Audit, ActionAdminRevoked); n != 1 {
		t.Fatalf("重复取消不应写审计，得 %d", n)
	}

	// 最后一个有效管理员不能取消，名单不变
	if err := h.S.RevokeAdmin(ctx, a); !errors.Is(err, errLastAdmin) || errs.As(err).Code != errs.Conflict {
		t.Fatalf("取消最后一个管理员应 409: %v", err)
	}
	// 假事务不回滚：这里只断言错误；真回滚由 PG 用例覆盖
}

func TestEnsureNotLastAdmin(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	only := h.Users.addUser("only@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(only)

	if err := h.S.EnsureNotLastAdmin(ctx, only); !errors.Is(err, errLastAdmin) {
		t.Fatalf("唯一管理员应 Conflict: %v", err)
	}

	other := h.Users.addUser("other@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(other)
	if err := h.S.EnsureNotLastAdmin(ctx, only); err != nil {
		t.Fatalf("两名活跃管理员应允许停用其一: %v", err)
	}

	u := h.Users.users[other]
	u.Status = "suspended"
	h.Users.users[other] = u
	if err := h.S.EnsureNotLastAdmin(ctx, only); !errors.Is(err, errLastAdmin) {
		t.Fatalf("另一管理员非活跃时仍应保护: %v", err)
	}

	plain := h.Users.addUser("plain@example.com", "pass", UserStatusActive)
	if err := h.S.EnsureNotLastAdmin(ctx, plain); err != nil {
		t.Fatalf("非管理员应直接通过: %v", err)
	}
}

func TestIsStaffAndHasAdmin(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	plain := h.Users.addUser("plain@example.com", "pass", UserStatusActive)
	off := h.Users.addUser("off@example.com", "pass", "suspended")
	h.Store.addAdmin(off)

	for id, want := range map[uuid.UUID]bool{plain: false, off: true} {
		got, err := h.S.IsStaff(ctx, id)
		if err != nil || got != want {
			t.Fatalf("IsStaff(%v)=%v err=%v want %v", id, got, err, want)
		}
	}
	// 名单里只有停用的账号：没有可登录的管理员
	if ok, err := h.S.HasAdmin(ctx); err != nil || ok {
		t.Fatalf("HasAdmin=%v err=%v want false", ok, err)
	}
	h.Store.addAdmin(plain)
	if ok, err := h.S.HasAdmin(ctx); err != nil || !ok {
		t.Fatalf("HasAdmin=%v err=%v want true", ok, err)
	}

	list, err := h.S.ListAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Email != "off@example.com" || list[1].Email != "plain@example.com" {
		t.Fatalf("ListAdmins=%+v", list)
	}
	if list[0].Status != "suspended" || list[0].CreatedAt.IsZero() {
		t.Fatalf("ListAdmins[0]=%+v", list[0])
	}
}

func TestAdminProfile(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("me@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(uid)
	name := "运维甲"
	h.Users.displayNames[uid] = &name
	tok := adminLogin(t, h, ctx, "me@example.com", "pass")
	p, err := h.S.Authenticate(ctx, authn.AudienceAdmin, tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	prof, err := h.S.AdminProfile(authn.With(ctx, p))
	if err != nil {
		t.Fatal(err)
	}
	if prof.UserID != uid || prof.Email != "me@example.com" || prof.DisplayName == nil || *prof.DisplayName != name {
		t.Fatalf("profile=%+v", prof)
	}
	if !prof.Reauthed {
		t.Fatal("刚用口令登录应算重认证过")
	}
}
