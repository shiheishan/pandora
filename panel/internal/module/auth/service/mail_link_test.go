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
	signupErr  error
	configErr  error
}

func (f *fakeMailSettings) Signup(ctx context.Context) (settingssvc.Signup, error) {
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

func wireMailLink(h *testHarness, settings *fakeMailSettings, mailer *fakeMailLinkMailer) {
	h.S.UseMailLink(settings, mailer, ratelimit.New(h.RDB), "https://panel.example.test")
}

func TestStartMailLoginUnavailable(t *testing.T) {
	h := newTestService(t)
	ctx := context.Background()

	if err := h.S.StartMailLogin(ctx, "user@example.com"); !errors.Is(err, ErrMailLinkUnavailable) {
		t.Fatalf("未注入端口: %v", err)
	}

	settings := &fakeMailSettings{
		signup:     settingssvc.Signup{MailLinkLogin: false},
		configured: true,
	}
	mailer := &fakeMailLinkMailer{}
	wireMailLink(h, settings, mailer)

	if err := h.S.StartMailLogin(ctx, "user@example.com"); !errors.Is(err, ErrMailLinkUnavailable) {
		t.Fatalf("开关关闭: %v", err)
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
	_, gotEpoch, ok, err := st.consumeMailLink(ctx, hashSecret(token))
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

	if err := h.S.StartMailLogin(ctx, "send@example.com"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("应发一封，得 %d", mailer.calls)
	}

	if err := h.S.StartMailLogin(ctx, "nobody@example.com"); err != nil {
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

	err := h.S.StartMailLogin(context.Background(), "bad")
	if errs.As(err).Code != errs.Invalid {
		t.Fatalf("格式错: %v", err)
	}
}
