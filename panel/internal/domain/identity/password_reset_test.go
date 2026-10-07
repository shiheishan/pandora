package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

type stubMailer struct{ configured bool }

func (stubMailer) EnqueueToAddress(context.Context, pgx.Tx, string, string, string, map[string]string, string) error {
	return errors.New("stub mailer must not be reached")
}
func (stubMailer) Kick()                                          {}
func (m stubMailer) EmailConfigured(context.Context, string) bool { return m.configured }

// 没配邮件服务时找回密码整体关闭：入口能力为 false，两步接口都拒绝，且不碰数据库（pool 为 nil）。
func TestPasswordResetClosedWithoutMail(t *testing.T) {
	ctx := context.Background()
	for _, svc := range []*Service{{}, {mailer: stubMailer{configured: false}}} {
		if svc.PasswordResetAvailable(ctx, "t") {
			t.Fatal("password reset must be unavailable without a configured mailer")
		}
		if _, err := svc.StartPasswordReset(ctx, "t", StartPasswordResetInput{Email: "a@example.test"}); !errors.Is(err, ErrPasswordResetUnavailable) {
			t.Fatalf("start err=%v", err)
		}
		err := svc.CompletePasswordReset(ctx, "t", CompletePasswordResetInput{
			Email: "a@example.test", Code: "123456", NewPassword: "abcd12345"})
		if !errors.Is(err, ErrPasswordResetUnavailable) {
			t.Fatalf("complete err=%v", err)
		}
	}
	if !(&Service{mailer: stubMailer{configured: true}}).PasswordResetAvailable(ctx, "t") {
		t.Fatal("configured mailer must open password reset")
	}
}

// 格式不对的验证码不开事务、回统一的错误；邮箱格式错回字段错误。
func TestPasswordResetRejectsMalformedInputBeforeDatabase(t *testing.T) {
	svc := &Service{mailer: stubMailer{configured: true}}
	ctx := context.Background()
	for _, code := range []string{"", "12345", "1234567", "12a456", "１２３４５６"} {
		err := svc.CompletePasswordReset(ctx, "t", CompletePasswordResetInput{
			Email: "a@example.test", Code: code, NewPassword: "abcd12345"})
		var he *httpx.Error
		if !errors.As(err, &he) || he.Fields["code"] == "" {
			t.Fatalf("code %q err=%v, want the code field error", code, err)
		}
	}
	_, err := svc.StartPasswordReset(ctx, "t", StartPasswordResetInput{Email: "not-an-email"})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Fields["email"] == "" {
		t.Fatalf("bad email err=%v", err)
	}
}

// 找回密码与后台替人重置共用同一份吊销实现，且都在审计之前吊销（用户定：重置后吊销全部登录）。
func TestPasswordResetRevokesThroughSharedHelper(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	for _, name := range []string{"Service.CompletePasswordReset", "Service.AdminResetPassword"} {
		body := pkg.Decl(name)
		revoke := strings.Index(body, "revokeAllLogins(ctx, tx, tenantID,")
		auditAt := strings.LastIndex(body, "audit.Write(")
		if revoke < 0 || auditAt < revoke {
			t.Fatalf("%s must revoke all logins via revokeAllLogins before its audit", name)
		}
	}
	helper := pkg.Decl("revokeAllLogins")
	for _, want := range []string{"UPDATE sessions", "UPDATE refresh_tokens", "credentialrevocation.RevokeRefreshFamilies"} {
		if !strings.Contains(helper, want) {
			t.Fatalf("revokeAllLogins lacks %q", want)
		}
	}
	complete := pkg.Decl("Service.CompletePasswordReset")
	for _, want := range []string{"FOR UPDATE", "attempts >= maxAttempts", "attempts = attempts + 1", "subtle.ConstantTimeCompare"} {
		if !strings.Contains(complete, want) {
			t.Fatalf("CompletePasswordReset lacks %q", want)
		}
	}
}

// 门户禁登只在口令核对通过之后判断：口令不对时回的仍是同一句「邮箱或密码不正确」。
func TestPortalStaffBlockComesAfterPasswordCheck(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	login := pkg.Decl("Service.Login")
	verify := strings.Index(login, "slot.Verify(in.Password, phc)")
	block := strings.Index(login, "ErrStaffPortalLogin")
	if verify < 0 || block < verify {
		t.Fatal("Login must reject staff on the portal only after the password verified")
	}
	if !strings.Contains(pkg.Decl("Service.ConsumeQuickLogin"), "ErrStaffPortalLogin") {
		t.Fatal("ConsumeQuickLogin must also refuse staff accounts")
	}
	for _, reason := range []string{"loginFailureInvalid", "loginFailureInactive", "loginFailureNoAdminRole", "loginFailureStaffPortal"} {
		if !strings.Contains(login, "fail(slot, "+reason) {
			t.Fatalf("Login does not audit the %s failure", reason)
		}
	}
	if strings.Contains(ErrStaffPortalLogin.Message, "/") {
		t.Fatal("the staff portal message must not carry any path")
	}
}
