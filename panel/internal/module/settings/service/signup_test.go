package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func newSignupService(st *kvStore, al *auditLog) *Service {
	return newService(st, fakeTx{}, nil, nil, al)
}

func TestSignupDefaultsEmptyStore(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newSignupService(st, &auditLog{})
	got, err := s.Signup(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.CaptchaProvider != "" || got.HasCaptchaSecret || got.CaptchaEnabled() {
		t.Fatalf("captcha: provider=%q has=%v", got.CaptchaProvider, got.HasCaptchaSecret)
	}
	if got.RecaptchaV3Threshold != 0.5 {
		t.Fatalf("threshold: %v", got.RecaptchaV3Threshold)
	}
	if got.EmailSuffixWhitelistEnabled || got.GmailAliasBlock {
		t.Fatalf("suffix/gmail flags")
	}
	wantSuffixes := []string{"gmail.com", "qq.com", "163.com", "yahoo.com", "sina.com", "126.com", "outlook.com", "yeah.net", "foxmail.com"}
	if len(got.EmailSuffixes) != len(wantSuffixes) {
		t.Fatalf("suffixes len %d", len(got.EmailSuffixes))
	}
	for i, w := range wantSuffixes {
		if got.EmailSuffixes[i] != w {
			t.Fatalf("suffix[%d]=%q want %q", i, got.EmailSuffixes[i], w)
		}
	}
	if !got.IPLimitEnabled || got.IPLimitCount != 10 || got.IPLimitWindowMinutes != 1 {
		t.Fatalf("ip limit: en=%v count=%d window=%d", got.IPLimitEnabled, got.IPLimitCount, got.IPLimitWindowMinutes)
	}
	if got.TrialPlanID != nil || got.TrialHours != 0 {
		t.Fatalf("trial: plan=%v hours=%d", got.TrialPlanID, got.TrialHours)
	}
	if got.InviteMaxCodes != 5 || !got.InviteNeverExpire || got.MailLinkLogin {
		t.Fatalf("invite/mail: max=%d never=%v mail=%v", got.InviteMaxCodes, got.InviteNeverExpire, got.MailLinkLogin)
	}
}

func TestSetSignupPartialUpdate(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newSignupService(st, al)
	ctx := context.Background()

	gmail := true
	count := 20
	if err := s.SetSignup(ctx, SignupUpdate{GmailAliasBlock: &gmail, IPLimitCount: &count}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Signup(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.GmailAliasBlock || got.IPLimitCount != 20 {
		t.Fatalf("updated: gmail=%v count=%d", got.GmailAliasBlock, got.IPLimitCount)
	}
	if !got.IPLimitEnabled || got.InviteMaxCodes != 5 {
		t.Fatalf("unchanged: ip_en=%v invite_max=%d", got.IPLimitEnabled, got.InviteMaxCodes)
	}
	// 保存成功后 SignupGeneration 加一（门户入口页 CSP 缓存按它失效）；没有改动、校验失败都不加
	if g := s.SignupGeneration(); g != 1 {
		t.Fatalf("generation after save = %d, want 1", g)
	}
	if err := s.SetSignup(ctx, SignupUpdate{GmailAliasBlock: &gmail}); err != nil {
		t.Fatalf("set same: %v", err)
	}
	bad := 0
	if err := s.SetSignup(ctx, SignupUpdate{IPLimitCount: &bad}); err == nil {
		t.Fatal("ip_limit_count 0 should be rejected")
	}
	if g := s.SignupGeneration(); g != 1 {
		t.Fatalf("generation after no-op / invalid save = %d, want 1", g)
	}
}

func TestSetSignupTrialHoursOutOfRange(t *testing.T) {
	s := newSignupService(&kvStore{kv: map[string]json.RawMessage{}}, &auditLog{})
	bad := 9000
	err := s.SetSignup(context.Background(), SignupUpdate{TrialHours: &bad})
	assertFieldValidation(t, err, "trial_hours")
}

func TestSetSignupInviteMaxCodesOutOfRange(t *testing.T) {
	s := newSignupService(&kvStore{kv: map[string]json.RawMessage{}}, &auditLog{})
	bad := 100
	err := s.SetSignup(context.Background(), SignupUpdate{InviteMaxCodes: &bad})
	assertFieldValidation(t, err, "invite_max_codes")
}

func TestSetSignupProviderWithoutSecret(t *testing.T) {
	s := newSignupService(&kvStore{kv: map[string]json.RawMessage{}}, &auditLog{})
	provider := CaptchaTurnstile
	siteKey := "pk_test"
	err := s.SetSignup(context.Background(), SignupUpdate{
		CaptchaProvider: &provider,
		CaptchaSiteKey:  &siteKey,
	})
	assertFieldValidation(t, err, "captcha_secret")
}

func TestSetSignupTrialHoursWithoutPlan(t *testing.T) {
	s := newSignupService(&kvStore{kv: map[string]json.RawMessage{}}, &auditLog{})
	hours := 24
	err := s.SetSignup(context.Background(), SignupUpdate{TrialHours: &hours})
	assertFieldValidation(t, err, "trial_plan_id")
}

func TestNormalizeEmailSuffixes(t *testing.T) {
	out, err := NormalizeEmailSuffixes([]string{"@QQ.com ", "qq.com", "foo.bar.com"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 || out[0] != "qq.com" || out[1] != "foo.bar.com" {
		t.Fatalf("got %v", out)
	}

	_, err = NormalizeEmailSuffixes([]string{"not a domain!"})
	assertFieldValidation(t, err, "email_suffixes")
}

func TestSetSignupWithTrialPlan(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newSignupService(st, &auditLog{})
	ctx := context.Background()
	plan := uuid.New().String()
	hours := 48
	if err := s.SetSignup(ctx, SignupUpdate{TrialPlanID: &plan, TrialHours: &hours}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Signup(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TrialPlanID == nil || got.TrialPlanID.String() != plan || got.TrialHours != 48 {
		t.Fatalf("trial: plan=%v hours=%d", got.TrialPlanID, got.TrialHours)
	}
}
