package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

type fakeMailSettings struct {
	signup     settingssvc.Signup
	configured bool
	emailOff   bool
	signupErr  error
	configErr  error
	// siteURL 是 SiteURL 的返回值；空时用 https://panel.example.test/
	siteURL string
}

func (f *fakeMailSettings) SwitchEnabled(_ context.Context, code string) (bool, error) {
	if code == settingssvc.SwitchNotifyEmail && f.emailOff {
		return false, nil
	}
	return true, nil
}

func (f *fakeMailSettings) SiteURL(context.Context) string {
	if f.siteURL == "" {
		return "https://panel.example.test/"
	}
	return f.siteURL
}

func (f *fakeMailSettings) SignupWithoutSecret(ctx context.Context) (settingssvc.Signup, error) {
	if f.signupErr != nil {
		return settingssvc.Signup{}, f.signupErr
	}
	return f.signup, nil
}

func (f *fakeMailSettings) MailConfigured(ctx context.Context) (bool, error) {
	if f.configErr != nil {
		return false, f.configErr
	}
	return f.configured, nil
}

type fakeMailLinkMailer struct {
	calls int
}

func (f *fakeMailLinkMailer) SendToAddress(ctx context.Context, templateCode, address string, vars map[string]string, userID *uuid.UUID) error {
	f.calls++
	return nil
}

// fakeMailCaptcha 记下每次校验的令牌与 action；err 非空时一律拒绝。
type fakeMailCaptcha struct {
	err     error
	tokens  []string
	actions []string
}

func (f *fakeMailCaptcha) VerifyCaptcha(_ context.Context, token, action string) error {
	f.tokens = append(f.tokens, token)
	f.actions = append(f.actions, action)
	return f.err
}

func wireMailLink(h *testHarness, settings *fakeMailSettings, mailer *fakeMailLinkMailer) *fakeMailCaptcha {
	c := &fakeMailCaptcha{}
	h.S.UseMailLink(settings, mailer, c, ratelimit.New(h.RDB))
	return c
}

func TestStartMailLoginUnavailable(t *testing.T) {
	h := newTestService(t)
	ctx := context.Background()

	if err := h.S.StartMailLogin(ctx, "user@example.com", ""); !errors.Is(err, ErrMailLinkUnavailable) {
		t.Fatalf("未注入端口: %v", err)
	}

	settings := &fakeMailSettings{
		signup:     settingssvc.Signup{MailLinkLogin: false},
		configured: true,
	}
	mailer := &fakeMailLinkMailer{}
	wireMailLink(h, settings, mailer)

	if err := h.S.StartMailLogin(ctx, "user@example.com", ""); !errors.Is(err, ErrMailLinkUnavailable) {
		t.Fatalf("开关关闭: %v", err)
	}
}

func TestStartMailLoginMailPaused(t *testing.T) {
	h := newTestService(t)
	ctx := context.Background()

	settings := &fakeMailSettings{
		signup:     settingssvc.Signup{MailLinkLogin: true},
		configured: true,
		emailOff:   true,
	}
	mailer := &fakeMailLinkMailer{}
	wireMailLink(h, settings, mailer)

	err := h.S.StartMailLogin(ctx, "user@example.com", "")
	if !errors.Is(err, errMailLinkMailPaused) {
		t.Fatalf("应 Is errMailLinkMailPaused, 得 %v", err)
	}
	if e := errs.As(err); e.Code != errs.Unavailable {
		t.Fatalf("code=%v want %v", e.Code, errs.Unavailable)
	}
	if mailer.calls != 0 {
		t.Fatalf("暂停时不应发信，calls=%d", mailer.calls)
	}
}

func TestConsumeMailLoginInvalidToken(t *testing.T) {
	h := newTestService(t)
	ctx := context.Background()

	_, err := h.S.ConsumeMailLogin(ctx, "")
	if !errors.Is(err, errMailLinkInvalid) {
		t.Fatalf("空 token: %v", err)
	}

	long := strings.Repeat("a", 129)
	_, err = h.S.ConsumeMailLogin(ctx, long)
	if !errors.Is(err, errMailLinkInvalid) {
		t.Fatalf("超长 token: %v", err)
	}
}

func TestStoreMailLink(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	uid := uuid.Must(uuid.NewV7())
	epoch := "0"
	ttl := mailLinkTTL

	h1 := hashSecret("mail-token-1")
	h2 := hashSecret("mail-token-2")

	if err := st.issueMailLink(ctx, uid, h1, epoch, ttl); err != nil {
		t.Fatalf("issue h1: %v", err)
	}
	if err := st.issueMailLink(ctx, uid, h2, epoch, ttl); err != nil {
		t.Fatalf("issue h2: %v", err)
	}
	if n, _ := rdb.Exists(ctx, mailLinkKey(h1)).Result(); n != 0 {
		t.Fatal("签第二条后第一条 ml 键应已删")
	}

	gotUID, gotEpoch, ok, err := st.consumeMailLink(ctx, h2)
	if err != nil || !ok || gotUID != uid || gotEpoch != epoch {
		t.Fatalf("消费 h2: ok=%v uid=%v epoch=%q err=%v", ok, gotUID, gotEpoch, err)
	}

	_, _, ok, err = st.consumeMailLink(ctx, h2)
	if err != nil || ok {
		t.Fatal("再次消费应 ok=false")
	}

	if err := st.issueMailLink(ctx, uid, h1, epoch, ttl); err != nil {
		t.Fatalf("re-issue h1: %v", err)
	}
	if err := rdb.Incr(ctx, epochKey(uid)).Err(); err != nil {
		t.Fatalf("INCR epoch: %v", err)
	}

	token := "mail-consume-epoch"
	if err := st.issueMailLink(ctx, uid, hashSecret(token), "1", ttl); err != nil {
		t.Fatalf("issue after epoch bump: %v", err)
	}
	_, gotEpoch, ok, err = st.consumeMailLink(ctx, hashSecret(token))
	if err != nil || !ok || gotEpoch != "1" {
		t.Fatalf("消费应带回签发纪元: ok=%v epoch=%q err=%v", ok, gotEpoch, err)
	}
}

func TestConsumeMailLoginEpochMismatch(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.1")
	uid := h.Users.addUser("link@example.com", "pass", UserStatusActive)
	epoch, err := h.S.sessions.epoch(ctx, uid)
	if err != nil {
		t.Fatalf("epoch: %v", err)
	}
	token := "mail-consume-ok"
	if err := h.S.sessions.issueMailLink(ctx, uid, hashSecret(token), epoch, mailLinkTTL); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := h.RDB.Incr(ctx, epochKey(uid)).Err(); err != nil {
		t.Fatalf("INCR epoch: %v", err)
	}

	_, err = h.S.ConsumeMailLogin(ctx, token)
	if !errors.Is(err, errMailLinkInvalid) {
		t.Fatalf("纪元变更后应无效: %v", err)
	}
}

func TestConsumeMailLoginSuccess(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.2")
	uid := h.Users.addUser("ok@example.com", "pass", UserStatusActive)
	epoch, err := h.S.sessions.epoch(ctx, uid)
	if err != nil {
		t.Fatalf("epoch: %v", err)
	}
	token := "mail-login-success"
	if err := h.S.sessions.issueMailLink(ctx, uid, hashSecret(token), epoch, mailLinkTTL); err != nil {
		t.Fatalf("issue: %v", err)
	}

	tok, err := h.S.ConsumeMailLogin(ctx, token)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if tok.AccessToken == "" || tok.UserID != uid {
		t.Fatalf("tokens: uid=%v access empty=%v", tok.UserID, tok.AccessToken == "")
	}
	if len(h.Audit.events) == 0 || h.Audit.events[len(h.Audit.events)-1].ev.Action != "auth.mail_link_login" {
		t.Fatal("应有 mail_link_login 审计")
	}
	if len(h.Users.touched) != 1 || h.Users.touched[0] != uid {
		t.Fatalf("TouchLastLogin: %v", h.Users.touched)
	}

	_, err = h.S.ConsumeMailLogin(ctx, token)
	if !errors.Is(err, errMailLinkInvalid) {
		t.Fatalf("二次消费: %v", err)
	}
}

func TestStartMailLoginSendsMail(t *testing.T) {
	h := newTestService(t)
	ctx := context.Background()
	_ = h.Users.addUser("send@example.com", "pass", UserStatusActive)

	settings := &fakeMailSettings{
		signup:     settingssvc.Signup{MailLinkLogin: true},
		configured: true,
	}
	mailer := &fakeMailLinkMailer{}
	wireMailLink(h, settings, mailer)

	if err := h.S.StartMailLogin(ctx, "send@example.com", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("应发一封，得 %d", mailer.calls)
	}

	if err := h.S.StartMailLogin(ctx, "nobody@example.com", ""); err != nil {
		t.Fatalf("不存在邮箱: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("不存在不应发信，calls=%d", mailer.calls)
	}
}

func TestStartMailLoginInvalidEmail(t *testing.T) {
	h := newTestService(t)
	settings := &fakeMailSettings{
		signup:     settingssvc.Signup{MailLinkLogin: true},
		configured: true,
	}
	wireMailLink(h, settings, &fakeMailLinkMailer{})

	err := h.S.StartMailLogin(context.Background(), "bad", "")
	if errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("格式错: %v", err)
	}
}

// 第 1 步先过人机验证（action 为 mail_login），失败时不发信、也不占按邮箱的限频额度。
func TestStartMailLoginCaptcha(t *testing.T) {
	h := newTestService(t)
	ctx := context.Background()
	_ = h.Users.addUser("cap@example.com", "pass", UserStatusActive)
	settings := &fakeMailSettings{signup: settingssvc.Signup{MailLinkLogin: true}, configured: true}
	mailer := &fakeMailLinkMailer{}
	c := wireMailLink(h, settings, mailer)

	c.err = errs.Invalid(map[string]string{"captcha": "人机验证未通过，请重试"})
	for i := 0; i < 5; i++ {
		err := h.S.StartMailLogin(ctx, "cap@example.com", "bad-token")
		if e := errs.As(err); e.Code != errs.ValidationFailed || e.Fields["captcha"] == "" {
			t.Fatalf("人机验证失败应 422 fields.captcha: %v", err)
		}
	}
	if mailer.calls != 0 {
		t.Fatalf("人机验证失败不应发信，calls=%d", mailer.calls)
	}
	if len(c.actions) != 5 || c.actions[0] != mailLinkCaptchaAction || c.tokens[0] != "bad-token" {
		t.Fatalf("captcha calls: tokens=%v actions=%v", c.tokens, c.actions)
	}

	// 人机验证在限频之前：前面 5 次失败没占掉同一邮箱 15 分钟 3 次的额度
	c.err = nil
	if err := h.S.StartMailLogin(ctx, "cap@example.com", "good-token"); err != nil {
		t.Fatalf("通过后应发信: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("应发一封，calls=%d", mailer.calls)
	}
}

// 防枚举：不存在、停用、已注销、管理员四种邮箱，与正常发信的响应一样（都返回 nil），且都不发信。
func TestStartMailLoginNoMailForIneligible(t *testing.T) {
	h := newTestService(t)
	ctx := context.Background()
	_ = h.Users.addUser("suspended@example.com", "pass", "suspended")
	_ = h.Users.addUser("deleted@example.com", "pass", "deleted")
	adminID := h.Users.addUser("staff@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(adminID)
	settings := &fakeMailSettings{signup: settingssvc.Signup{MailLinkLogin: true}, configured: true}
	mailer := &fakeMailLinkMailer{}
	wireMailLink(h, settings, mailer)

	for _, email := range []string{"nobody@example.com", "suspended@example.com", "deleted@example.com", "staff@example.com"} {
		if err := h.S.StartMailLogin(ctx, email, ""); err != nil {
			t.Fatalf("%s: 应与正常发信同样返回 nil: %v", email, err)
		}
	}
	if mailer.calls != 0 {
		t.Fatalf("四种邮箱都不应发信，calls=%d", mailer.calls)
	}
}

// 吊销全部会话会推进凭据纪元，之前签发的邮件登录链接随之失效（后台改邮箱、注销、改口令都走这条）。
func TestRevokeUserInvalidatesMailLink(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.3")
	uid := h.Users.addUser("revoke@example.com", "pass", UserStatusActive)
	epoch, err := h.S.sessions.epoch(ctx, uid)
	if err != nil {
		t.Fatalf("epoch: %v", err)
	}
	token := "mail-revoked"
	if err := h.S.sessions.issueMailLink(ctx, uid, hashSecret(token), epoch, mailLinkTTL); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := h.S.RevokeUser(ctx, uid, ""); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := h.S.ConsumeMailLogin(ctx, token); !errors.Is(err, errMailLinkInvalid) {
		t.Fatalf("吊销后旧链接应无效: %v", err)
	}
}
