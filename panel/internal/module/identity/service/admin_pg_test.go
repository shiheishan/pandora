package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func adminCtx(t *testing.T, e *env, ctx context.Context) context.Context {
	a := e.mkUser(t, ctx, uniqEmail(t, "adm"), "Passw0rdLong1")
	return asAdmin(ctx, a.ID)
}

func TestPGListUsers(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	emails := []string{uniqEmail(t, "a"), uniqEmail(t, "b"), uniqEmail(t, "c")}
	var suspended uuid.UUID
	for i, em := range emails {
		u := e.mkUser(t, ctx, em, "Passw0rdLong1")
		if i == 2 {
			suspended = u.ID
			if err := e.svc.SetStatus(actx, u.ID, StatusSuspended, "pg test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	frag := strings.Split(emails[0], "@")[0]
	rows, total, err := e.svc.ListUsers(actx, UserFilter{Query: frag}, 50, 0)
	if err != nil || total < 1 || len(rows) < 1 {
		t.Fatalf("frag total=%d rows=%d err=%v", total, len(rows), err)
	}
	uByEmail, err := e.svc.FindByEmail(ctx, emails[1])
	if err != nil {
		t.Fatal(err)
	}
	rows, total, err = e.svc.ListUsers(actx, UserFilter{Query: emails[1]}, 50, 0)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != uByEmail.ID {
		t.Fatalf("by email total=%d rows=%d id=%v want %v err=%v", total, len(rows), rows[0].ID, uByEmail.ID, err)
	}
	u1 := uByEmail
	rows, total, err = e.svc.ListUsers(actx, UserFilter{Query: u1.ID.String()}, 50, 0)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != u1.ID {
		t.Fatalf("by id total=%d err=%v", total, err)
	}
	rows, _, err = e.svc.ListUsers(actx, UserFilter{Statuses: []string{StatusSuspended}}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ID == suspended {
			found = true
		}
	}
	if !found {
		t.Fatalf("suspended list %+v", rows)
	}
	_, _, err = e.svc.ListUsers(actx, UserFilter{Statuses: []string{"bogus"}}, 50, 0)
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("bad status: %v", err)
	}
	pct := "%"
	rows, total, err = e.svc.ListUsers(actx, UserFilter{Query: pct}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(len(rows)) {
		t.Fatalf("total %d rows %d", total, len(rows))
	}
}

func TestPGUserDetail(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	_, err := e.svc.UserDetail(actx, uuid.New())
	if codeOf(err) != errs.NotFound {
		t.Fatalf("%v", err)
	}
	ref := e.mkUser(t, ctx, uniqEmail(t, "ref"), "Passw0rdLong1")
	inv, _, err := e.svc.MyInvite(ctx, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	setRegistration(t, actx, e.settings, settingssvc.RegistrationOpen, false)
	start, err := e.svc.StartRegistration(ctx, uniqEmail(t, "child"), inv.Code)
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.UserDetail(actx, child.ID)
	if err != nil || d.Referrer == nil || d.Referrer.ID != ref.ID {
		t.Fatalf("referrer %+v err=%v", d.Referrer, err)
	}
	cnt, err := e.svc.UserDetail(actx, ref.ID)
	if err != nil || cnt.ReferralCount < 1 {
		t.Fatalf("ref count %d err=%v", cnt.ReferralCount, err)
	}
}

func TestPGSetStatus(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	adminID, _ := authn.From(actx)
	target := e.mkUser(t, ctx, uniqEmail(t, "tgt"), "Passw0rdLong1")

	if err := e.svc.SetStatus(actx, adminID.UserID, StatusSuspended, "x"); codeOf(err) != errs.Conflict {
		t.Fatalf("self: %v", err)
	}
	if err := e.svc.SetStatus(actx, target.ID, StatusSuspended, ""); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("no reason: %v", err)
	}
	if err := e.svc.SetStatus(actx, target.ID, StatusSuspended, "bad\x00"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("nul reason: %v", err)
	}

	e.guard.err = nil
	e.revoker.calls = nil
	if err := e.svc.SetStatus(actx, target.ID, StatusSuspended, "ops"); err != nil {
		t.Fatal(err)
	}
	if len(e.guard.calls) != 1 || e.guard.calls[0] != target.ID {
		t.Fatalf("guard %+v", e.guard.calls)
	}
	if len(e.revoker.calls) != 1 {
		t.Fatalf("revoke %+v", e.revoker.calls)
	}
	if e.auditCount(ctx, ActionStatusChange, target.ID) != 1 {
		t.Fatal("audit status")
	}
	_, err := e.svc.Authenticate(ctx, authn.AudiencePublic, target.Email, "Passw0rdLong1")
	if !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("auth after suspend: %v", err)
	}

	e.guard.err = errs.New(errs.Conflict)
	other := e.mkUser(t, ctx, uniqEmail(t, "guard"), "Passw0rdLong1")
	if err := e.svc.SetStatus(actx, other.ID, StatusSuspended, "x"); codeOf(err) != errs.Conflict {
		t.Fatalf("guard block: %v", err)
	}
	u, _ := e.svc.Get(ctx, other.ID)
	if u.Status != StatusActive {
		t.Fatalf("status %s", u.Status)
	}
	if len(e.revoker.calls) != 1 {
		t.Fatal("no extra revoke")
	}

	e.staff.manageErr = errs.New(errs.Forbidden)
	victim := e.mkUser(t, ctx, uniqEmail(t, "staff"), "Passw0rdLong1")
	if err := e.svc.SetStatus(actx, victim.ID, StatusSuspended, "x"); codeOf(err) != errs.Forbidden {
		t.Fatalf("staff manage: %v", err)
	}
	e.staff.manageErr = nil

	if err := e.svc.SetStatus(actx, target.ID, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	beforeRevokes := len(e.revoker.calls)
	if err := e.svc.SetStatus(actx, target.ID, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	if len(e.revoker.calls) != beforeRevokes {
		t.Fatal("reactivate should not revoke")
	}

	norev := e.build()
	norev.UseAdminGuard(e.guard)
	norev.UseStaffGuard(e.staff)
	if err := norev.SetStatus(actx, victim.ID, StatusBanned, "no revoker"); codeOf(err) != errs.Internal {
		t.Fatalf("no revoker: %v", err)
	}
}

func TestPGAdminResetPassword(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	adminID, _ := authn.From(actx)
	target := e.mkUser(t, ctx, uniqEmail(t, "rst"), "Passw0rdLong1")

	if err := e.svc.AdminResetPassword(actx, adminID.UserID, "NewPassw0rd9", "x"); codeOf(err) != errs.BadRequest {
		t.Fatalf("self: %v", err)
	}
	newPw := "AnotherPass1"
	e.revoker.calls = nil
	if err := e.svc.AdminResetPassword(actx, target.ID, newPw, "helpdesk"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, authn.AudiencePublic, target.Email, "Passw0rdLong1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, authn.AudiencePublic, target.Email, newPw); err != nil {
		t.Fatal(err)
	}
	if len(e.revoker.calls) != 1 || e.revoker.calls[0].user != target.ID {
		t.Fatalf("revoke %+v", e.revoker.calls)
	}
	if e.auditCount(ctx, ActionPasswordResetByAdmin, target.ID) != 1 {
		t.Fatal("audit")
	}

	e.staff.staff[target.ID] = true
	if err := e.svc.AdminResetPassword(actx, target.ID, "short1", "x"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("staff short: %v", err)
	}
}

func TestPGGroups(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)

	gid, err := e.svc.CreateGroup(actx, "", "VIP Users", "")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := e.svc.ListGroups(actx)
	if err != nil {
		t.Fatal(err)
	}
	var vip Group
	for _, g := range groups {
		if g.ID == gid {
			vip = g
		}
	}
	if vip.Code != "vip-users" || vip.Users != 0 {
		t.Fatalf("vip %+v", vip)
	}
	u := e.mkUser(t, ctx, uniqEmail(t, "vipu"), "Passw0rdLong1")
	if err := e.svc.AssignGroup(actx, u.ID, &gid); err != nil {
		t.Fatal(err)
	}
	groups, _ = e.svc.ListGroups(actx)
	for _, g := range groups {
		if g.ID == gid && g.Users != 1 {
			t.Fatalf("users %d", g.Users)
		}
	}
	rows, _, err := e.svc.ListUsers(actx, UserFilter{Query: u.Email}, 10, 0)
	if err != nil || len(rows) != 1 || rows[0].GroupName != "VIP Users" {
		t.Fatalf("group name %+v err=%v", rows, err)
	}
	if err := e.svc.DeleteGroup(actx, gid); codeOf(err) != errs.Conflict {
		t.Fatalf("delete with users: %v", err)
	}
	if err := e.svc.AssignGroup(actx, u.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteGroup(actx, gid); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteGroup(actx, gid); codeOf(err) != errs.NotFound {
		t.Fatalf("delete again: %v", err)
	}
	if err := e.svc.UpdateGroup(actx, gid, "VIP Renamed", "note"); codeOf(err) != errs.NotFound {
		t.Fatalf("update deleted group: %v", err)
	}
	gid2, err := e.svc.CreateGroup(actx, "tmp", "Tmp", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UpdateGroup(actx, gid2, "Tmp2", ""); err != nil {
		t.Fatal(err)
	}
}

// 库层约束报错会让整个测试事务作废（TxCtx 不开保存点），所以撞唯一键、撞外键的断言各自单独成用例、放在最后一步。
func TestPGGroupDuplicateCode(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	if _, err := e.svc.CreateGroup(actx, "", "VIP Users", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateGroup(actx, "vip-users", "Other", ""); codeOf(err) != errs.Conflict {
		t.Fatalf("dup code: %v", err)
	}
}

func TestPGAssignMissingGroup(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	u := e.mkUser(t, ctx, uniqEmail(t, "nogrp"), "Passw0rdLong1")
	fake := uuid.New()
	if err := e.svc.AssignGroup(actx, u.ID, &fake); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("bad group: %v", err)
	}
}

func TestPGDirectory(t *testing.T) {
	e, ctx := newEnv(t)
	u := e.mkUser(t, ctx, uniqEmail(t, "dir"), "Passw0rdLong1")
	m, err := e.svc.Emails(ctx, []uuid.UUID{u.ID})
	if err != nil || m[u.ID] != u.Email {
		t.Fatalf("emails %+v err=%v", m, err)
	}
	frag := strings.Split(u.Email, "-")[0]
	ids, err := e.svc.IDsByEmail(ctx, frag, 0)
	if err != nil || len(ids) < 1 {
		t.Fatalf("ids %v err=%v", ids, err)
	}
	ids2, err := e.svc.IDsByEmail(ctx, "", 0)
	if err != nil || len(ids2) != 0 {
		t.Fatalf("empty q: %v %v", ids2, err)
	}
	labels, err := e.svc.UserLabels(ctx, []uuid.UUID{u.ID})
	if err != nil || labels[u.ID] != u.Email {
		t.Fatalf("labels %+v", labels)
	}
}

func TestPGPurgeRegistrations(t *testing.T) {
	e, ctx := newEnv(t)
	old := time.Now().Add(-8 * 24 * time.Hour)
	for i := 0; i < 2; i++ {
		h := make([]byte, 32)
		if _, err := rand.Read(h); err != nil {
			t.Fatal(err)
		}
		email := uniqEmail(t, fmt.Sprintf("purge%d", i))
		q := `INSERT INTO registration_sessions (token_hash, email, status, expires_at, created_at) VALUES (?, ?, 'pending', ?, ?)`
		if err := e.exec(ctx, q, h, email, old, old); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	n, err := e.svc.PurgeRegistrations(ctx, cutoff, 10)
	if err != nil || n != 2 {
		t.Fatalf("purged %d err=%v", n, err)
	}
	_, err = e.svc.PurgeRegistrations(ctx, cutoff, 0)
	if err == nil {
		t.Fatal("batch 0 should error")
	}
}
