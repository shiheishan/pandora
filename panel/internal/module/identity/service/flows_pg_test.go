package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestPGRegistrationOpenNoVerify(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx, uniqEmail(t, "regadmin"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationOpen, false)

	email := uniqEmail(t, "New")
	start, err := e.svc.StartRegistration(ctx, " "+strings.ToUpper(email)+" ", "")
	if err != nil {
		t.Fatal(err)
	}
	if start.DevCode != "" || start.VerificationRequired {
		t.Fatalf("start %+v", start)
	}

	u, err := e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != strings.ToLower(email) {
		t.Fatalf("email %q", u.Email)
	}
	_, err = e.svc.Authenticate(ctx, authn.AudiencePublic, email, "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if e.auditCount(ctx, ActionRegistered, u.ID) != 1 {
		t.Fatalf("audit registered")
	}
	if _, err = e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345"); codeOf(err) != errs.Forbidden {
		t.Fatalf("reuse token: %v", err)
	}
}

func TestPGRegistrationEmailVerify(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx, uniqEmail(t, "veradmin"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationOpen, true)
	email := uniqEmail(t, "verify")

	start, err := e.svc.StartRegistration(ctx, email, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.mailer.sent) != 1 || e.mailer.sent[0].template != TemplateEmailVerify || e.mailer.sent[0].userID != nil {
		t.Fatalf("mail %+v", e.mailer.sent)
	}
	if start.DevCode == "" || start.DevCode != e.mailer.sent[0].vars["code"] {
		t.Fatalf("dev code %q vars %v", start.DevCode, e.mailer.sent[0].vars)
	}

	_, err = e.svc.CompleteRegistration(ctx, start.Token, "000000", "abc12345")
	if codeOf(err) != errs.Forbidden {
		t.Fatalf("wrong code: %v", err)
	}
	var attempts int
	if err := e.row(ctx, `SELECT attempts FROM registration_sessions WHERE email = ? AND status = 'pending'`, email).Scan(&attempts); err != nil || attempts < 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}

	u, err := e.svc.CompleteRegistration(ctx, start.Token, start.DevCode, "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	var verified bool
	if err := e.row(ctx, `SELECT email_verified_at IS NOT NULL FROM users WHERE id = ?`, u.ID).Scan(&verified); err != nil || !verified {
		t.Fatalf("verified=%v err=%v", verified, err)
	}
}

func TestPGStartRegistrationExistingEmail(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx, uniqEmail(t, "existadm"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationOpen, true)
	email := uniqEmail(t, "taken")
	e.mkUser(t, ctx, email, "Passw0rdLong1")

	e.mailer.sent = nil
	start, err := e.svc.StartRegistration(ctx, email, "")
	if err != nil {
		t.Fatal(err)
	}
	if start.Token == "" || start.DevCode != "" || len(e.mailer.sent) != 0 {
		t.Fatalf("token=%q dev=%q mails=%d", start.Token, start.DevCode, len(e.mailer.sent))
	}
}

func TestPGRegistrationClosed(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx, uniqEmail(t, "clsadm"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationClosed, false)
	_, err := e.svc.StartRegistration(ctx, uniqEmail(t, "closed"), "")
	if !errors.Is(err, ErrRegistrationUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestPGRegistrationInviteOnly(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx, uniqEmail(t, "invadm"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationInviteOnly, false)

	_, err := e.svc.StartRegistration(ctx, uniqEmail(t, "noinv"), "")
	if codeOf(err) != errs.Forbidden {
		t.Fatalf("no invite: %v", err)
	}

	inviter := e.mkUser(t, ctx, uniqEmail(t, "inviter"), "Passw0rdLong1")
	inv, invitees, err := e.svc.MyInvite(ctx, inviter.ID)
	if err != nil {
		t.Fatal(err)
	}
	newEmail := uniqEmail(t, "invited")
	start, err := e.svc.StartRegistration(ctx, newEmail, inv.Code)
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	var refCnt int64
	if err := e.row(ctx, `SELECT count(*) FROM referrals WHERE referrer_user_id = ?`, inviter.ID).Scan(&refCnt); err != nil || refCnt != 1 {
		t.Fatalf("referrals %d err=%v", refCnt, err)
	}
	var used int
	if err := e.row(ctx, `SELECT used_count FROM invite_codes WHERE code = ?`, inv.Code).Scan(&used); err != nil || used != 1 {
		t.Fatalf("used_count=%d err=%v", used, err)
	}
	_, invitees2, err := e.svc.MyInvite(ctx, inviter.ID)
	if err != nil || len(invitees2) != 1 || !strings.Contains(invitees2[0].Email, "***") {
		t.Fatalf("invitees %+v err=%v", invitees2, err)
	}
	_ = u
	_ = invitees
}

func TestPGCompleteRegistrationWeakPassword(t *testing.T) {
	e, ctx := newEnv(t)
	if _, err := e.svc.CompleteRegistration(ctx, "whatever", "", "short"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("%v", err)
	}
}

func TestPGPasswordResetFlow(t *testing.T) {
	e, ctx := newEnv(t)
	email := uniqEmail(t, "reset")
	u := e.mkUser(t, ctx, email, "Passw0rdLong1")

	_, err := e.svc.StartPasswordReset(ctx, email)
	if !errors.Is(err, ErrPasswordResetUnavailable) {
		t.Fatalf("no smtp: %v", err)
	}

	mailAdmin := e.mkUser(t, ctx, uniqEmail(t, "mailadm"), "Passw0rdLong1")
	enableMail(t, asAdmin(ctx, mailAdmin.ID), e.settings)
	e.mailer.sent = nil
	start, err := e.svc.StartPasswordReset(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.mailer.sent) != 1 || e.mailer.sent[0].template != TemplatePasswordReset || e.mailer.sent[0].userID != nil {
		t.Fatalf("mail %+v", e.mailer.sent)
	}
	code := start.DevCode

	e.mailer.sent = nil
	_, err = e.svc.StartPasswordReset(ctx, uniqEmail(t, "ghost"))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.mailer.sent) != 0 {
		t.Fatal("ghost should not mail")
	}

	if err = e.svc.CompletePasswordReset(ctx, email, code, "short1"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("weak first: %v", err)
	}
	if err = e.svc.CompletePasswordReset(ctx, email, "000000", "NewPassw0rd9"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("wrong code: %v", err)
	}
	newPw := "NewPassw0rd9"
	if err = e.svc.CompletePasswordReset(ctx, email, code, newPw); err != nil {
		t.Fatal(err)
	}
	if len(e.revoker.calls) != 1 || e.revoker.calls[0].user != u.ID || e.revoker.calls[0].except != "" {
		t.Fatalf("revoke %+v", e.revoker.calls)
	}
	if _, err = e.svc.Authenticate(ctx, authn.AudiencePublic, email, "Passw0rdLong1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old pw: %v", err)
	}
	if _, err = e.svc.Authenticate(ctx, authn.AudiencePublic, email, newPw); err != nil {
		t.Fatal(err)
	}
	if e.auditCount(ctx, ActionPasswordReset, u.ID) != 1 {
		t.Fatal("audit reset")
	}
	if err = e.svc.CompletePasswordReset(ctx, email, code, "AnotherPass1"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("reuse code: %v", err)
	}

	e.staff.staff[u.ID] = true
	e.mailer.sent = nil
	_, err = e.svc.StartPasswordReset(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.mailer.sent) != 0 {
		t.Fatal("staff no mail")
	}
}

func TestPGChangePassword(t *testing.T) {
	e, ctx := newEnv(t)
	email := uniqEmail(t, "chgpw")
	oldPw := "Passw0rdLong1"
	u := e.mkUser(t, ctx, email, oldPw)
	uctx := asUser(ctx, u.ID)

	if err := e.svc.ChangePassword(uctx, "wrong", "NewPassw0rd9"); !errors.Is(err, ErrWrongCurrentPassword) {
		t.Fatalf("wrong old: %v", err)
	}
	var failCnt int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ? AND outcome = 'failure'`,
		ActionPasswordChanged, u.ID).Scan(&failCnt); err != nil || failCnt != 1 {
		t.Fatalf("fail audit %d err=%v", failCnt, err)
	}

	if err := e.svc.ChangePassword(uctx, oldPw, oldPw); codeOf(err) != errs.BadRequest {
		t.Fatalf("same: %v", err)
	}
	if err := e.svc.ChangePassword(uctx, oldPw, "short1"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("weak new: %v", err)
	}

	newPw := "NewPassw0rd9"
	if err := e.svc.ChangePassword(uctx, oldPw, newPw); err != nil {
		t.Fatal(err)
	}
	if len(e.revoker.calls) != 1 || e.revoker.calls[0].except != "s-cur" {
		t.Fatalf("revoke %+v", e.revoker.calls)
	}
	if _, err := e.svc.Authenticate(ctx, authn.AudiencePublic, email, newPw); err != nil {
		t.Fatal(err)
	}

	admin := e.mkUser(t, ctx, uniqEmail(t, "admch"), "Passw0rdLong1")
	actx := asAdmin(ctx, admin.ID)
	e.revoker.calls = nil
	if err := e.svc.ChangePassword(actx, "Passw0rdLong1", "AnotherPass1"); err != nil {
		t.Fatal(err)
	}
	if len(e.revoker.calls) != 1 || e.revoker.calls[0].except != "" {
		t.Fatalf("admin revoke %+v", e.revoker.calls)
	}

	u2 := e.mkUser(t, ctx, uniqEmail(t, "ratelimit"), oldPw)
	u2ctx := asUser(ctx, u2.ID)
	for i := 0; i < 5; i++ {
		_ = e.svc.ChangePassword(u2ctx, "bad", "NewPassw0rd9")
	}
	if err := e.svc.ChangePassword(u2ctx, oldPw, "NewPassw0rd9"); codeOf(err) != errs.RateLimited {
		t.Fatalf("6th: %v", err)
	}
}

func TestPGAuthenticate(t *testing.T) {
	e, ctx := newEnv(t)
	ghost := uniqEmail(t, "ghost")
	u, err := e.svc.Authenticate(ctx, authn.AudiencePublic, ghost, "any")
	if !errors.Is(err, ErrInvalidCredentials) || u.ID != uuid.Nil {
		t.Fatalf("ghost u=%v err=%v", u.ID, err)
	}

	email := uniqEmail(t, "auth")
	real := e.mkUser(t, ctx, email, "Passw0rdLong1")
	u, err = e.svc.Authenticate(ctx, authn.AudiencePublic, email, "wrong")
	if !errors.Is(err, ErrInvalidCredentials) || u.ID != real.ID {
		t.Fatalf("bad pw u=%v err=%v", u.ID, err)
	}

	susp := e.mkUser(t, ctx, uniqEmail(t, "susp"), "Passw0rdLong1")
	statusAdmin := e.mkUser(t, ctx, uniqEmail(t, "statusadm"), "Passw0rdLong1")
	if err := e.svc.SetStatus(asAdmin(ctx, statusAdmin.ID), susp.ID, StatusSuspended, "test"); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Authenticate(ctx, authn.AudiencePublic, susp.Email, "Passw0rdLong1")
	if !errors.Is(err, ErrAccountInactive) || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("inactive: %v", err)
	}

	_, err = e.svc.Authenticate(ctx, authn.AudiencePublic, email, "Passw0rdLong1")
	if err != nil {
		t.Fatal(err)
	}
	// Authenticate 不记登录时间；由 auth 建好会话后调 TouchLastLogin
	var loginAt *time.Time
	if err := e.row(ctx, `SELECT last_login_at FROM users WHERE id = ?`, real.ID).Scan(&loginAt); err != nil || loginAt != nil {
		t.Fatalf("Authenticate 不应记 login_at: %v err=%v", loginAt, err)
	}
	if err := e.svc.TouchLastLogin(ctx, real.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT last_login_at FROM users WHERE id = ?`, real.ID).Scan(&loginAt); err != nil || loginAt == nil {
		t.Fatalf("login_at=%v err=%v", loginAt, err)
	}

	if _, err = e.svc.Authenticate(ctx, "bogus", email, "Passw0rdLong1"); codeOf(err) != errs.BadRequest {
		t.Fatalf("audience: %v", err)
	}
}

func TestPGAuthenticateRehashWeakPHC(t *testing.T) {
	e, ctx := newEnv(t)
	email := uniqEmail(t, "rehash")
	pw := "Passw0rdLong1"
	u := e.mkUser(t, ctx, email, pw)
	phc := weakPHC(t, pw)
	if err := e.exec(ctx, `UPDATE user_passwords SET phc = ? WHERE user_id = ?`, phc, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, authn.AudiencePublic, email, pw); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := e.row(ctx, `SELECT phc FROM user_passwords WHERE user_id = ?`, u.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "m=19456") {
		t.Fatalf("phc %q", stored)
	}
}

// 注册第 2 步：令牌无效直接拒（进事务前的预检），不报别的错。
func TestPGCompleteRegistrationUnknownToken(t *testing.T) {
	e, ctx := newEnv(t)
	if _, err := e.svc.CompleteRegistration(ctx, "no-such-token", "", "abc12345"); !errors.Is(err, ErrRegistrationUnavailable) {
		t.Fatalf("%v", err)
	}
}

// 注册验证码：再次 start 作废旧码；验错累加该码 attempts，满 max_attempts 作废。
func TestPGRegistrationCodeRotationAndAttempts(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx, uniqEmail(t, "rotadm"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationOpen, true)
	email := uniqEmail(t, "rotate")
	target := e.svc.targetHash(email)
	const liveSQL = `SELECT count(*) FROM verification_codes WHERE purpose = 'email_verify' AND target_hash = ? AND consumed_at IS NULL AND expires_at > now()`

	if _, err := e.svc.StartRegistration(ctx, email, ""); err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.StartRegistration(ctx, email, "")
	if err != nil {
		t.Fatal(err)
	}
	var live int64
	if err := e.row(ctx, liveSQL, target).Scan(&live); err != nil || live != 1 {
		t.Fatalf("再次 start 后有效码应只剩 1 条，实际 %d err=%v", live, err)
	}

	wrong := "000000"
	if wrong == second.DevCode {
		wrong = "111111"
	}
	for i := 0; i < registrationMaxAttempts; i++ {
		if _, err := e.svc.CompleteRegistration(ctx, second.Token, wrong, "abc12345"); !errors.Is(err, ErrRegistrationUnavailable) {
			t.Fatalf("第 %d 次错码: %v", i+1, err)
		}
		if i == 0 {
			var attempts int
			if err := e.row(ctx, `SELECT attempts FROM verification_codes WHERE purpose = 'email_verify' AND target_hash = ? AND consumed_at IS NULL AND expires_at > now()`,
				target).Scan(&attempts); err != nil || attempts != 1 {
				t.Fatalf("码 attempts=%d err=%v", attempts, err)
			}
		}
	}
	if err := e.row(ctx, liveSQL, target).Scan(&live); err != nil || live != 0 {
		t.Fatalf("错满后该邮箱的码应作废，剩 %d err=%v", live, err)
	}
}

// 匿名流程的审计：没有主体时操作者记成本人；有主体时记主体。
func TestPGSelfAuditActor(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx, uniqEmail(t, "audadm"), "Passw0rdLong1")
	setRegistration(t, asAdmin(ctx, admin.ID), e.settings, settingssvc.RegistrationOpen, false)

	start, err := e.svc.StartRegistration(ctx, uniqEmail(t, "anon"), "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	var actor *uuid.UUID
	if err := e.row(ctx, `SELECT actor_id FROM audit_events WHERE action = ? AND resource_id = ?`, ActionRegistered, u.ID).Scan(&actor); err != nil || actor == nil || *actor != u.ID {
		t.Fatalf("匿名注册 actor=%v err=%v", actor, err)
	}

	other := e.mkUser(t, ctx, uniqEmail(t, "logged"), "Passw0rdLong1")
	start2, err := e.svc.StartRegistration(ctx, uniqEmail(t, "withp"), "")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := e.svc.CompleteRegistration(asUser(ctx, other.ID), start2.Token, "", "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT actor_id FROM audit_events WHERE action = ? AND resource_id = ?`, ActionRegistered, u2.ID).Scan(&actor); err != nil || actor == nil || *actor != other.ID {
		t.Fatalf("带主体 actor=%v err=%v", actor, err)
	}
}

// 登录时静默重算哈希不动 rotated_at。
func TestPGRehashKeepsRotatedAt(t *testing.T) {
	e, ctx := newEnv(t)
	email := uniqEmail(t, "rot")
	pw := "Passw0rdLong1"
	u := e.mkUser(t, ctx, email, pw)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := e.exec(ctx, `UPDATE user_passwords SET phc = ?, rotated_at = ? WHERE user_id = ?`, weakPHC(t, pw), old, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, authn.AudiencePublic, email, pw); err != nil {
		t.Fatal(err)
	}
	var phc string
	var rotated time.Time
	if err := e.row(ctx, `SELECT phc, rotated_at FROM user_passwords WHERE user_id = ?`, u.ID).Scan(&phc, &rotated); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(phc, "m=19456") || !rotated.Equal(old) {
		t.Fatalf("phc=%q rotated=%v", phc, rotated)
	}
}
