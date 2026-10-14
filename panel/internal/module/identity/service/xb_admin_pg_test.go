package service

import (
	"context"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 注销相关假端口。
type fakeCloser struct {
	n     int
	calls []uuid.UUID
}

func (f *fakeCloser) CloseUserSubscriptions(_ context.Context, userID uuid.UUID, _ string) (int, error) {
	f.calls = append(f.calls, userID)
	return f.n, nil
}

type fakeUnbinder struct {
	calls []uuid.UUID
}

func (f *fakeUnbinder) UnbindTelegram(_ context.Context, userID uuid.UUID) error {
	f.calls = append(f.calls, userID)
	return errs.New(errs.NotFound)
}

func TestPGDeleteUser(t *testing.T) {
	e, ctx := newEnv(t)
	closer := &fakeCloser{n: 3}
	unbind := &fakeUnbinder{}
	e.svc.UseAccountCloser(closer)
	e.svc.UseTelegramUnbinder(unbind)

	actx := adminCtx(t, e, ctx)
	target := e.mkUser(t, ctx, uniqEmail(t, "del"), "Passw0rdLong1")
	_, _, err := e.svc.MyInvite(asUser(ctx, target.ID), target.ID)
	if err != nil {
		t.Fatal(err)
	}

	e.revoker.calls = nil
	res, err := e.svc.DeleteUser(actx, target.ID, "用户申请注销")
	if err != nil {
		t.Fatal(err)
	}
	if res.ShortID != DeletedShortID(target.ID) || res.TerminatedSubscriptions != 3 {
		t.Fatalf("res %+v", res)
	}

	var status, email string
	var deletedAt *string
	var dn, note *string
	if err := e.row(ctx, `SELECT status, email, deleted_at::text, display_name, admin_note FROM users WHERE id = ?`, target.ID).
		Scan(&status, &email, &deletedAt, &dn, &note); err != nil {
		t.Fatal(err)
	}
	if status != StatusDeleted || email != DeletedEmail(target.ID) || deletedAt == nil || dn != nil || note != nil {
		t.Fatalf("user row status=%s email=%s deleted=%v dn=%v note=%v", status, email, deletedAt, dn, note)
	}
	var pwCnt int64
	if err := e.row(ctx, `SELECT count(*) FROM user_passwords WHERE user_id = ?`, target.ID).Scan(&pwCnt); err != nil || pwCnt != 0 {
		t.Fatalf("passwords %d err=%v", pwCnt, err)
	}
	var invStatus string
	if err := e.row(ctx, `SELECT status FROM invite_codes WHERE owner_user_id = ?`, target.ID).Scan(&invStatus); err != nil || invStatus != "disabled" {
		t.Fatalf("invite %q err=%v", invStatus, err)
	}
	if len(e.revoker.calls) != 1 || len(closer.calls) != 1 || len(unbind.calls) != 1 {
		t.Fatalf("revoker=%d closer=%d unbind=%d", len(e.revoker.calls), len(closer.calls), len(unbind.calls))
	}
	if e.auditCount(ctx, ActionDeleted, target.ID) != 1 {
		t.Fatal("audit deleted")
	}

	if _, err = e.svc.DeleteUser(actx, target.ID, "again reason"); codeOf(err) != errs.Conflict {
		t.Fatalf("delete again: %v", err)
	}
	if err = e.svc.SetStatus(actx, target.ID, StatusSuspended, "x reason"); codeOf(err) != errs.Conflict {
		t.Fatalf("set status deleted: %v", err)
	}
	if err = e.svc.AdminSetEmail(actx, target.ID, uniqEmail(t, "new"), "email reason"); codeOf(err) != errs.Conflict {
		t.Fatalf("set email deleted: %v", err)
	}
	if err = e.svc.AdminSetNote(actx, target.ID, nil, strPtr("note")); codeOf(err) != errs.Conflict {
		t.Fatalf("set note deleted: %v", err)
	}

	adminID, _ := authn.From(actx)
	if _, err = e.svc.DeleteUser(actx, adminID.UserID, "self delete"); codeOf(err) != errs.Conflict {
		t.Fatalf("self: %v", err)
	}
	staff := e.mkUser(t, ctx, uniqEmail(t, "staffdel"), "Passw0rdLong1")
	e.staff.staff[staff.ID] = true
	if _, err = e.svc.DeleteUser(actx, staff.ID, "staff delete"); codeOf(err) != errs.Conflict {
		t.Fatalf("staff: %v", err)
	}
	if _, err = e.svc.DeleteUser(actx, target.ID, "短"); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["reason"] == "" {
		t.Fatalf("short reason: %v", err)
	}

	bare := e.build()
	bare.UseStaffGuard(e.staff)
	victim := e.mkUser(t, ctx, uniqEmail(t, "noclose"), "Passw0rdLong1")
	if _, err = bare.DeleteUser(actx, victim.ID, "no closer wired"); codeOf(err) != errs.Internal {
		t.Fatalf("no closer: %v", err)
	}
}

func TestPGAdminSetEmail(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	u := e.mkUser(t, ctx, uniqEmail(t, "em"), "Passw0rdLong1")
	if err := e.exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}

	newEm := uniqEmail(t, "changed")
	if err := e.svc.AdminSetEmail(actx, u.ID, newEm, "用户要求换绑"); err != nil {
		t.Fatal(err)
	}
	var verified *string
	var stored string
	if err := e.row(ctx, `SELECT email, email_verified_at::text FROM users WHERE id = ?`, u.ID).Scan(&stored, &verified); err != nil {
		t.Fatal(err)
	}
	if stored != newEm || verified != nil {
		t.Fatalf("email=%s verified=%v", stored, verified)
	}
	if e.auditCount(ctx, ActionEmailChanged, u.ID) != 1 {
		t.Fatal("audit email")
	}

	beforeAudit := e.auditCount(ctx, ActionEmailChanged, u.ID)
	if err := e.svc.AdminSetEmail(actx, u.ID, strings.ToUpper(newEm), "noop case"); err != nil {
		t.Fatal(err)
	}
	if e.auditCount(ctx, ActionEmailChanged, u.ID) != beforeAudit {
		t.Fatal("same email case should not audit")
	}

	other := e.mkUser(t, ctx, uniqEmail(t, "other"), "Passw0rdLong1")
	if err := e.svc.AdminSetEmail(actx, u.ID, other.Email, "steal email"); codeOf(err) != errs.Conflict {
		t.Fatalf("dup: %v", err)
	}
	if err := e.svc.AdminSetEmail(actx, u.ID, "x@deleted.invalid", "bad domain"); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["email"] == "" {
		t.Fatalf("deleted.invalid: %v", err)
	}
	if err := e.svc.AdminSetEmail(actx, u.ID, "not-email", "bad fmt"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("format: %v", err)
	}
}

func TestPGAdminSetNote(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	u := e.mkUser(t, ctx, uniqEmail(t, "note"), "Passw0rdLong1")
	origName := "原名"
	if err := e.exec(ctx, `UPDATE users SET display_name = ? WHERE id = ?`, origName, u.ID); err != nil {
		t.Fatal(err)
	}

	noteText := "后台备注内容"
	if err := e.svc.AdminSetNote(actx, u.ID, nil, &noteText); err != nil {
		t.Fatal(err)
	}
	var dn, note *string
	if err := e.row(ctx, `SELECT display_name, admin_note FROM users WHERE id = ?`, u.ID).Scan(&dn, &note); err != nil {
		t.Fatal(err)
	}
	if dn == nil || *dn != origName || note == nil || *note != noteText {
		t.Fatalf("dn=%v note=%v", dn, note)
	}

	empty := ""
	if err := e.svc.AdminSetNote(actx, u.ID, nil, &empty); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT admin_note FROM users WHERE id = ?`, u.ID).Scan(&note); err != nil || note != nil {
		t.Fatalf("cleared note %v err=%v", note, err)
	}

	if err := e.svc.AdminSetNote(actx, u.ID, nil, nil); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("both nil: %v", err)
	}

	longNote := strings.Repeat("备", 2001)
	if err := e.svc.AdminSetNote(actx, u.ID, nil, &longNote); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["admin_note"] == "" {
		t.Fatalf("long note: %v", err)
	}
	longDN := strings.Repeat("名", 65)
	if err := e.svc.AdminSetNote(actx, u.ID, &longDN, nil); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["display_name"] == "" {
		t.Fatalf("long dn: %v", err)
	}

	secret := "不应出现在审计里的备注全文"
	if err := e.svc.AdminSetNote(actx, u.ID, nil, &secret); err != nil {
		t.Fatal(err)
	}
	var dataText string
	if err := e.row(ctx, `SELECT data::text FROM audit_events WHERE action = ? AND resource_id = ? ORDER BY created_at DESC LIMIT 1`,
		ActionNoteChanged, u.ID).Scan(&dataText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dataText, secret) {
		t.Fatalf("audit leaked note: %s", dataText)
	}
}

func TestPGAdminSetReferrer(t *testing.T) {
	e, ctx := newEnv(t)
	e.svc.UseAccountCloser(&fakeCloser{})
	e.svc.UseTelegramUnbinder(&fakeUnbinder{})
	actx := adminCtx(t, e, ctx)
	reason := "调整邀请关系测试"

	a := e.mkUser(t, ctx, uniqEmail(t, "refA"), "Passw0rdLong1")
	b := e.mkUser(t, ctx, uniqEmail(t, "refB"), "Passw0rdLong1")
	c := e.mkUser(t, ctx, uniqEmail(t, "refC"), "Passw0rdLong1")
	if err := e.exec(ctx, `INSERT INTO referrals (referee_user_id, referrer_user_id) VALUES (?, ?)`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `INSERT INTO referrals (referee_user_id, referrer_user_id) VALUES (?, ?)`, c.ID, b.ID); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.AdminSetReferrer(actx, a.ID, &c.ID, reason); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["referrer_id"] == "" {
		t.Fatalf("cycle A<-C: %v", err)
	}
	if err := e.svc.AdminSetReferrer(actx, c.ID, &c.ID, reason); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["referrer_id"] == "" {
		t.Fatalf("self referrer: %v", err)
	}

	if err := e.svc.AdminSetReferrer(actx, c.ID, &a.ID, reason); err != nil {
		t.Fatal(err)
	}
	var effRef uuid.UUID
	if err := e.row(ctx, `SELECT referrer_user_id FROM effective_referrals WHERE referee_user_id = ?`, c.ID).Scan(&effRef); err != nil || effRef != a.ID {
		t.Fatalf("effective C->A: %v err=%v", effRef, err)
	}
	var prevRef *uuid.UUID
	if err := e.row(ctx, `SELECT previous_referrer_user_id FROM referral_changes WHERE referee_user_id = ?`, c.ID).Scan(&prevRef); err != nil || prevRef == nil || *prevRef != b.ID {
		t.Fatalf("change prev=%v err=%v", prevRef, err)
	}
	var chCnt int64
	if err := e.row(ctx, `SELECT count(*) FROM referral_changes WHERE referee_user_id = ?`, c.ID).Scan(&chCnt); err != nil || chCnt != 1 {
		t.Fatalf("changes %d err=%v", chCnt, err)
	}

	if err := e.svc.AdminSetReferrer(actx, c.ID, &a.ID, reason); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT count(*) FROM referral_changes WHERE referee_user_id = ?`, c.ID).Scan(&chCnt); err != nil || chCnt != 1 {
		t.Fatalf("same referrer no row: %d err=%v", chCnt, err)
	}

	if err := e.svc.AdminSetReferrer(actx, c.ID, nil, reason); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT count(*) FROM effective_referrals WHERE referee_user_id = ?`, c.ID).Scan(&chCnt); err != nil || chCnt != 0 {
		t.Fatalf("cleared effective %d err=%v", chCnt, err)
	}
	if e.auditCount(ctx, ActionReferrerChanged, c.ID) != 2 {
		t.Fatalf("audit referrer changed")
	}

	gone := e.mkUser(t, ctx, uniqEmail(t, "gone"), "Passw0rdLong1")
	if _, err := e.svc.DeleteUser(actx, gone.ID, "注销测试用户"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AdminSetReferrer(actx, c.ID, &gone.ID, reason); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["referrer_id"] == "" {
		t.Fatalf("deleted referrer: %v", err)
	}
}

func TestPGBulkBan(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	tag := bulkTag(t)
	adminEm := uniqEmail(t, tag+"-adm")
	adminU := e.mkUser(t, ctx, adminEm, "Passw0rdLong1")
	actx := asAdmin(ctx, adminU.ID)

	uActive := e.mkUser(t, ctx, uniqEmail(t, tag+"-ok"), "Passw0rdLong1")
	uBanned := e.mkUser(t, ctx, uniqEmail(t, tag+"-ban"), "Passw0rdLong1")
	uStaff := e.mkUser(t, ctx, uniqEmail(t, tag+"-staff"), "Passw0rdLong1")
	if err := e.svc.SetStatus(actx, uBanned.ID, StatusBanned, "pre banned"); err != nil {
		t.Fatal(err)
	}
	e.staff.staff[uStaff.ID] = true

	q := BulkFilter{Query: tag}
	prev, err := e.svc.PreviewBulk(actx, q)
	if err != nil {
		t.Fatal(err)
	}
	expected := prev.Total
	if expected < 4 {
		t.Fatalf("expected >=4 matched, got %d", expected)
	}

	e.revoker.calls = nil
	res, err := e.svc.BulkBan(actx, q, expected, "批量封禁测试原因")
	if err != nil {
		t.Fatal(err)
	}
	if res.Banned != 1 {
		t.Fatalf("banned %d want 1", res.Banned)
	}
	if res.Skipped[BanSkipAlreadyBanned] != 1 || res.Skipped[BanSkipAdmin] != 1 || res.Skipped[BanSkipSelf] != 1 {
		t.Fatalf("skipped %+v", res.Skipped)
	}
	got, err := e.svc.Get(ctx, uActive.ID)
	if err != nil || got.Status != StatusBanned {
		t.Fatalf("active user status %s err=%v", got.Status, err)
	}
	if len(e.revoker.calls) != 1 || e.revoker.calls[0].user != uActive.ID {
		t.Fatalf("revoke %+v", e.revoker.calls)
	}

	if _, err = e.svc.BulkBan(actx, q, expected+1, "count mismatch"); codeOf(err) != errs.Conflict {
		t.Fatalf("expected total: %v", err)
	}
	if _, err = e.svc.BulkBan(actx, BulkFilter{}, 0, "empty filter"); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["filter"] == "" {
		t.Fatalf("empty filter: %v", err)
	}
	if _, err = e.svc.BulkBan(actx, q, expected, "短"); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["reason"] == "" {
		t.Fatalf("short reason: %v", err)
	}
	var bulkAudit int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionBulkBanned).Scan(&bulkAudit); err != nil || bulkAudit != 1 {
		t.Fatalf("bulk audit %d err=%v", bulkAudit, err)
	}
}

func strPtr(s string) *string { return &s }
