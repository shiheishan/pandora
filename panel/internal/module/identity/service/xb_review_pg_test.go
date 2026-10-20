package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 后台改邮箱：旧邮箱上的找回码作废、会话全部吊销（推进纪元，邮件登录链接随之失效）；审计不记明文邮箱。
func TestPGAdminSetEmailInvalidatesOldEmail(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	enableMail(t, actx, e.settings)

	oldEmail := uniqEmail(t, "oldmail")
	u := e.mkUser(t, ctx, oldEmail, "Passw0rdLong1")
	start, err := e.svc.StartPasswordReset(ctx, oldEmail, "")
	if err != nil || start.DevCode == "" {
		t.Fatalf("reset start: %+v %v", start, err)
	}

	newEmail := uniqEmail(t, "newmail")
	e.revoker.calls = nil
	if err := e.svc.AdminSetEmail(actx, u.ID, newEmail, "用户报告旧邮箱被盗"); err != nil {
		t.Fatal(err)
	}
	if len(e.revoker.calls) != 1 || e.revoker.calls[0].user != u.ID || e.revoker.calls[0].except != "" {
		t.Fatalf("改邮箱应吊销全部会话: %+v", e.revoker.calls)
	}

	// 攻击者拿旧邮箱和改之前收到的码去重置：必须被拒
	if err := e.svc.CompletePasswordReset(ctx, oldEmail, start.DevCode, "Hijack3dPass9"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("旧邮箱的找回码应失效: %v", err)
	}

	var data string
	if err := e.row(ctx, `SELECT data::text FROM audit_events WHERE action = ? AND resource_id = ? ORDER BY occurred_at DESC, id DESC LIMIT 1`,
		ActionEmailChanged, u.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, oldEmail) || strings.Contains(data, newEmail) {
		t.Fatalf("审计不应带明文邮箱: %s", data)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatal(err)
	}
	if d["before_hmac"] != e.svc.auditEmailHMAC(oldEmail) || d["after_hmac"] != e.svc.auditEmailHMAC(newEmail) {
		t.Fatalf("审计应带邮箱 HMAC 供对照: %s", data)
	}
	if d["before"] != maskEmail(oldEmail) || d["after"] != maskEmail(newEmail) {
		t.Fatalf("审计应带打码邮箱: %s", data)
	}
}

// 找回第 2 步再核一次：码对应的邮箱必须仍是用户现在的邮箱（别处改了邮箱、没作废码时也挡住）。
func TestPGPasswordResetRequiresCurrentEmail(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	enableMail(t, actx, e.settings)

	oldEmail := uniqEmail(t, "cur")
	u := e.mkUser(t, ctx, oldEmail, "Passw0rdLong1")
	start, err := e.svc.StartPasswordReset(ctx, oldEmail, "")
	if err != nil {
		t.Fatal(err)
	}
	// 绕过 service 直接改库里的邮箱（码没有被作废）
	if err := e.exec(ctx, `UPDATE users SET email = ? WHERE id = ?`, uniqEmail(t, "moved"), u.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CompletePasswordReset(ctx, oldEmail, start.DevCode, "Hijack3dPass9"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("邮箱已不是这个人的，码不能用: %v", err)
	}
}

// 占位邮箱的域名（任何大小写）在注册、命令行建号、后台改邮箱都被拒，免得有人抢注让别人注销不了。
func TestPGReservedEmailRejected(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	setRegistration(t, actx, e.settings, settingssvc.RegistrationOpen, false)

	victim := e.mkUser(t, ctx, uniqEmail(t, "victim"), "Passw0rdLong1")
	squat := DeletedEmail(victim.ID)
	for _, em := range []string{squat, strings.ToUpper(squat), "someone@Deleted.Invalid"} {
		if _, err := e.svc.StartRegistration(ctx, em, "", ""); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["email"] == "" {
			t.Fatalf("注册 %s 应 422 email: %v", em, err)
		}
		if _, err := e.svc.CreateUser(ctx, em, "Passw0rdLong1"); codeOf(err) != errs.ValidationFailed {
			t.Fatalf("建号 %s 应 422: %v", em, err)
		}
		if err := e.svc.AdminSetEmail(actx, victim.ID, em, "测试占位邮箱后缀"); codeOf(err) != errs.ValidationFailed {
			t.Fatalf("改邮箱 %s 应 422: %v", em, err)
		}
	}
}

// useFakeCaptchaV3 开 reCAPTCHA v3 并换上假服务商：令牌就是 action（用例里约定），回同站点的 hostname。
func useFakeCaptchaV3(t *testing.T, e *env, actx context.Context) {
	t.Helper()
	provider, siteKey, secret, threshold := settingssvc.CaptchaRecaptchaV3, "test-site-key", "test-secret", 0.5
	if err := e.settings.SetSignup(actx, settingssvc.SignupUpdate{
		CaptchaProvider: &provider, CaptchaSiteKey: &siteKey, CaptchaSecret: &secret, RecaptchaV3Threshold: &threshold,
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "score": 0.9, "hostname": testCaptchaHost, "action": r.PostForm.Get("response"),
		})
	}))
	t.Cleanup(srv.Close)
	e.svc.captcha = &captchaVerifier{client: srv.Client(), recaptchaURL: srv.URL, turnstileURL: srv.URL, siteURLs: testSiteURLs}
}

// 注册、找回第 1 步的人机验证接线：各自带自己的 action（v3 令牌挪到别的入口不能用），空令牌不放行。
func TestPGCaptchaWiring(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	setRegistration(t, actx, e.settings, settingssvc.RegistrationOpen, false)
	enableMail(t, actx, e.settings)
	useFakeCaptchaV3(t, e, actx)

	if _, err := e.svc.StartRegistration(ctx, uniqEmail(t, "capreg"), "", ""); codeOf(err) != errs.ValidationFailed || fieldsOf(err)["captcha"] == "" {
		t.Fatalf("注册空令牌应 422 captcha: %v", err)
	}
	if _, err := e.svc.StartRegistration(ctx, uniqEmail(t, "capreg"), "", captchaActionPasswordReset); fieldsOf(err)["captcha"] == "" {
		t.Fatalf("注册用找回的令牌应被拒: %v", err)
	}
	if _, err := e.svc.StartRegistration(ctx, uniqEmail(t, "capreg"), "", captchaActionRegister); err != nil {
		t.Fatalf("注册的令牌应通过: %v", err)
	}

	email := uniqEmail(t, "capreset")
	e.mkUser(t, ctx, email, "Passw0rdLong1")
	if _, err := e.svc.StartPasswordReset(ctx, email, ""); fieldsOf(err)["captcha"] == "" {
		t.Fatalf("找回空令牌应 422 captcha: %v", err)
	}
	if _, err := e.svc.StartPasswordReset(ctx, email, captchaActionRegister); fieldsOf(err)["captcha"] == "" {
		t.Fatalf("找回用注册的令牌应被拒: %v", err)
	}
	if _, err := e.svc.StartPasswordReset(ctx, email, captchaActionPasswordReset); err != nil {
		t.Fatalf("找回的令牌应通过: %v", err)
	}
	if err := e.svc.VerifyCaptcha(ctx, captchaActionMailLogin, CaptchaActionMailLogin); err != nil {
		t.Fatalf("邮件登录的令牌应通过: %v", err)
	}
	if err := e.svc.VerifyCaptcha(ctx, captchaActionRegister, CaptchaActionMailLogin); fieldsOf(err)["captcha"] == "" {
		t.Fatalf("邮件登录用注册的令牌应被拒: %v", err)
	}
}

// IsDeleted：不存在回 false、正常账号 false、注销后 true（别的模块写入口靠它拒已注销账号）。
func TestPGIsDeleted(t *testing.T) {
	e, ctx := newEnv(t)
	if deleted, err := e.svc.IsDeleted(ctx, uuid.New()); err != nil || deleted {
		t.Fatalf("不存在：%v %v", deleted, err)
	}
	u := e.mkUser(t, ctx, uniqEmail(t, "isdel"), "Passw0rdLong1")
	if deleted, err := e.svc.IsDeleted(ctx, u.ID); err != nil || deleted {
		t.Fatalf("正常账号：%v %v", deleted, err)
	}
	if err := e.exec(ctx, `UPDATE users SET status = 'deleted', deleted_at = now() WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if deleted, err := e.svc.IsDeleted(ctx, u.ID); err != nil || !deleted {
		t.Fatalf("已注销：%v %v", deleted, err)
	}
}

// 找回第 1 步先过人机验证、再按邮箱限频：人机验证失败 3 次不占这个邮箱的额度，带合法令牌的第 4 次仍能发码；
// 合法请求照常计数，用满 3 次后再来回 429。
func TestPGPasswordResetCaptchaFailuresDontConsumeLimit(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	enableMail(t, actx, e.settings)
	useFakeCaptchaV3(t, e, actx)
	email := uniqEmail(t, "capquota")
	e.mkUser(t, ctx, email, "Passw0rdLong1")

	for i := 0; i < 3; i++ {
		if _, err := e.svc.StartPasswordReset(ctx, email, "garbage-token"); fieldsOf(err)["captcha"] == "" {
			t.Fatalf("第 %d 次垃圾令牌应 422 captcha: %v", i+1, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := e.svc.StartPasswordReset(ctx, email, captchaActionPasswordReset); err != nil {
			t.Fatalf("人机验证失败后第 %d 次合法请求应能发码: %v", i+1, err)
		}
	}
	if _, err := e.svc.StartPasswordReset(ctx, email, captchaActionPasswordReset); codeOf(err) != errs.RateLimited {
		t.Fatalf("合法请求用满额度后应 429: %v", err)
	}
}
