package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
)

// 注册试用假端口。
type fakeTrials struct {
	calls []trialCall
}

type trialCall struct {
	userID uuid.UUID
	planID uuid.UUID
	hours  int
}

func (f *fakeTrials) GrantSignupTrial(_ context.Context, userID, planID uuid.UUID, hours int) error {
	f.calls = append(f.calls, trialCall{userID: userID, planID: planID, hours: hours})
	return nil
}

// signupAdminCtx 建一个管理员并返回以他登录的上下文（改设置要写审计）。
func signupAdminCtx(t *testing.T, e *env, ctx context.Context) context.Context {
	t.Helper()
	admin := e.mkUser(t, ctx, uniqEmail(t, "signupadm"), "Passw0rdLong1")
	return asAdmin(ctx, admin.ID)
}

func TestPGInviteCodes(t *testing.T) {
	e, ctx := newEnv(t)
	actx := signupAdminCtx(t, e, ctx)
	maxCodes := 2
	if err := e.settings.SetSignup(actx, settingssvc.SignupUpdate{InviteMaxCodes: &maxCodes}); err != nil {
		t.Fatal(err)
	}

	u := e.mkUser(t, ctx, uniqEmail(t, "invcode"), "Passw0rdLong1")
	uctx := asUser(ctx, u.ID)

	inv, _, err := e.svc.MyInvite(uctx, u.ID)
	if err != nil || inv.Code == "" {
		t.Fatalf("MyInvite lazy: %+v err=%v", inv, err)
	}

	first, err := e.svc.CreateInviteCode(uctx, u.ID)
	if err != nil || first.Code == "" || first.Status != "active" {
		t.Fatalf("first create: %+v err=%v", first, err)
	}
	if _, err = e.svc.CreateInviteCode(uctx, u.ID); codeOf(err) != errs.Conflict {
		t.Fatalf("third active over max: %v", err)
	}

	list, err := e.svc.MyInviteCodes(uctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if list.MaxCodes != 2 || len(list.Codes) != 2 {
		t.Fatalf("list max=%d codes=%d", list.MaxCodes, len(list.Codes))
	}
	if list.Codes[0].Status != "active" || list.Codes[1].Status != "active" {
		t.Fatalf("both active: %+v", list.Codes)
	}
	if list.SingleUse {
		t.Fatalf("SingleUse default want false got %v", list.SingleUse)
	}
}

func TestPGRecordInviteVisit(t *testing.T) {
	e, ctx := newEnv(t)
	owner := e.mkUser(t, ctx, uniqEmail(t, "visit"), "Passw0rdLong1")
	inv, _, err := e.svc.MyInvite(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := inv.Code

	visitCnt := func() int64 {
		var n int64
		if err := e.row(ctx, `SELECT visit_count FROM invite_codes WHERE code = ?`, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	ipCtx := reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.7", UserAgent: "test-agent/1.0"})
	if err := e.svc.RecordInviteVisit(ipCtx, code); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordInviteVisit(ipCtx, code); err != nil {
		t.Fatal(err)
	}
	if visitCnt() != 1 {
		t.Fatalf("same IP twice want visit_count=1 got %d", visitCnt())
	}

	ip2Ctx := reqmeta.With(ctx, reqmeta.Meta{IP: "198.51.100.9", UserAgent: "test-agent/1.0"})
	if err := e.svc.RecordInviteVisit(ip2Ctx, code); err != nil {
		t.Fatal(err)
	}
	if visitCnt() != 2 {
		t.Fatalf("second IP want visit_count=2 got %d", visitCnt())
	}

	// 不存在的码、含非法字符的码：静默不计。
	if err := e.svc.RecordInviteVisit(ipCtx, "NOPECODE"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordInviteVisit(ipCtx, "BAD!!ZZ"); err != nil {
		t.Fatal(err)
	}
	if visitCnt() != 2 {
		t.Fatalf("unknown or malformed code should not bump: %d", visitCnt())
	}

	// 小写带空格的码能识别：换一个没计过的 IP，应加到 3。
	ip3Ctx := reqmeta.With(ctx, reqmeta.Meta{IP: "192.0.2.15", UserAgent: "test-agent/1.0"})
	if err := e.svc.RecordInviteVisit(ip3Ctx, "  "+strings.ToLower(code)+"  "); err != nil {
		t.Fatal(err)
	}
	if visitCnt() != 3 {
		t.Fatalf("lowercase padded code want visit_count=3 got %d", visitCnt())
	}
}

func TestPGRegistrationEmailPolicy(t *testing.T) {
	e, ctx := newEnv(t)
	actx := signupAdminCtx(t, e, ctx)
	setRegistration(t, actx, e.settings, settingssvc.RegistrationOpen, false)

	enabled := true
	suffixes := []string{"example.test"}
	if err := e.settings.SetSignup(actx, settingssvc.SignupUpdate{
		EmailSuffixWhitelistEnabled: &enabled,
		EmailSuffixes:               &suffixes,
	}); err != nil {
		t.Fatal(err)
	}

	okEmail := uniqEmail(t, "policy")
	start, err := e.svc.StartRegistration(ctx, okEmail, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345"); err != nil {
		t.Fatalf("example.test ok: %v", err)
	}

	_, err = e.svc.StartRegistration(ctx, "x@other.test", "", "")
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["email"] == "" {
		t.Fatalf("other suffix: %v", err)
	}

	gmailBlock := true
	gmailSuffixes := []string{"gmail.com"}
	if err := e.settings.SetSignup(actx, settingssvc.SignupUpdate{
		EmailSuffixWhitelistEnabled: &enabled,
		EmailSuffixes:               &gmailSuffixes,
		GmailAliasBlock:             &gmailBlock,
	}); err != nil {
		t.Fatal(err)
	}

	for _, em := range []string{"a.b@gmail.com", "a+b@gmail.com"} {
		_, err = e.svc.StartRegistration(ctx, em, "", "")
		if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["email"] == "" {
			t.Fatalf("gmail alias %s: %v", em, err)
		}
	}
	start, err = e.svc.StartRegistration(ctx, "ab@gmail.com", "", "")
	if err != nil {
		t.Fatalf("plain gmail: %v", err)
	}
	if _, err = e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345"); err != nil {
		t.Fatalf("complete gmail: %v", err)
	}
}

func TestPGRegistrationGrantsTrial(t *testing.T) {
	e, ctx := newEnv(t)
	actx := signupAdminCtx(t, e, ctx)
	setRegistration(t, actx, e.settings, settingssvc.RegistrationOpen, false)

	trials := &fakeTrials{}
	e.svc.UseTrialGranter(trials)
	plan := uuid.New()
	planStr := plan.String()
	hours := 24
	if err := e.settings.SetSignup(actx, settingssvc.SignupUpdate{
		TrialPlanID: &planStr,
		TrialHours:  &hours,
	}); err != nil {
		t.Fatal(err)
	}

	email1 := uniqEmail(t, "trial1")
	start, err := e.svc.StartRegistration(ctx, email1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	u1, err := e.svc.CompleteRegistration(ctx, start.Token, "", "abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if len(trials.calls) != 1 {
		t.Fatalf("trial calls %d", len(trials.calls))
	}
	if trials.calls[0].userID != u1.ID || trials.calls[0].planID != plan || trials.calls[0].hours != 24 {
		t.Fatalf("trial call %+v want user=%s plan=%s h=24", trials.calls[0], u1.ID, plan)
	}

	zero := 0
	if err := e.settings.SetSignup(actx, settingssvc.SignupUpdate{TrialHours: &zero}); err != nil {
		t.Fatal(err)
	}
	email2 := uniqEmail(t, "trial2")
	start2, err := e.svc.StartRegistration(ctx, email2, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.CompleteRegistration(ctx, start2.Token, "", "abc12345"); err != nil {
		t.Fatal(err)
	}
	if len(trials.calls) != 1 {
		t.Fatalf("trial off should not add calls: %d", len(trials.calls))
	}

	bare := e.build()
	if err := e.settings.SetSignup(actx, settingssvc.SignupUpdate{TrialHours: &hours, TrialPlanID: &planStr}); err != nil {
		t.Fatal(err)
	}
	email3 := uniqEmail(t, "notrial")
	start3, err := bare.StartRegistration(ctx, email3, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = bare.CompleteRegistration(ctx, start3.Token, "", "abc12345")
	if codeOf(err) != errs.Internal {
		t.Fatalf("no granter: %v", err)
	}
	// 生产里 CompleteRegistration 自己开事务，返回错误即整体回滚、用户不会建出来；
	// 测试跑在 testkit.TxCtx 的外层事务里，tx.Do 嵌套时直接复用外层事务、没有保存点，
	// 看不到这次回滚，所以这里只断言错误码。
}

func TestPGInviteSingleUse(t *testing.T) {
	e, ctx := newEnv(t)
	actx := signupAdminCtx(t, e, ctx)
	setRegistration(t, actx, e.settings, settingssvc.RegistrationInviteOnly, false)
	never := false
	if err := e.settings.SetSignup(actx, settingssvc.SignupUpdate{InviteNeverExpire: &never}); err != nil {
		t.Fatal(err)
	}

	a := e.mkUser(t, ctx, uniqEmail(t, "invA"), "Passw0rdLong1")
	inv, _, err := e.svc.MyInvite(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := inv.Code
	codes, err := e.svc.MyInviteCodes(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !codes.SingleUse {
		t.Fatalf("SingleUse want true got %v", codes.SingleUse)
	}

	bEmail := uniqEmail(t, "invB")
	startB, err := e.svc.StartRegistration(ctx, bEmail, code, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.CompleteRegistration(ctx, startB.Token, "", "abc12345"); err != nil {
		t.Fatalf("B register: %v", err)
	}

	var status string
	if err := e.row(ctx, `SELECT status FROM invite_codes WHERE code = ?`, code).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "active" {
		t.Fatalf("code should not stay active after single use, status=%s", status)
	}
	if status != "exhausted" {
		t.Fatalf("expected exhausted, got %s", status)
	}

	cEmail := uniqEmail(t, "invC")
	_, err = e.svc.StartRegistration(ctx, cEmail, code, "")
	if !errors.Is(err, ErrRegistrationUnavailable) && codeOf(err) != errs.Forbidden {
		t.Fatalf("C reuse code: %v", err)
	}
}
