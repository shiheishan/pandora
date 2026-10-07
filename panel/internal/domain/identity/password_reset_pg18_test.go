package identity

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

// mailOffMailer 是「没配邮件服务」的投递出口：找回密码必须整体关闭。
type mailOffMailer struct{ *notify.Service }

func (mailOffMailer) EmailConfigured(context.Context, string) bool { return false }

// 持有后台角色的账号不能登录门户；登录失败按 IP 与账号聚合限频地记审计。
func TestPortalStaffLoginPG18(t *testing.T) {
	ctx, admin, app := openLogoutPG18Fixture(t)
	f := seedAuthorityFixture(t, ctx, admin, "d5a10000-0000-4000-8000-", "portal-staff-login-pg18")
	svc := &Service{
		pool:       app,
		issuer:     token.NewIssuer("public", []byte("portal-staff-pg18-secret-32bytes"), time.Hour),
		refreshTTL: 24 * time.Hour,
		hashSalt:   []byte("portal-staff-pg18-hash"),
	}
	login := func(email, password, ip string) (*LoginOutput, error) {
		return svc.Login(ctx, f.tenant, LoginInput{
			Email: email, Password: password, IPHash: []byte(ip), IP: "", UserAgent: "pg18",
		})
	}

	if out, err := login("plain@authority.invalid", authorityPassword, "ip-1"); err != nil || out.AccessToken == "" {
		t.Fatalf("plain user portal login: out=%v err=%v", out, err)
	}
	// 租户级与 self 范围的生效绑定都算「持有后台角色」
	for _, tc := range []struct{ email, ip string }{
		{"super@authority.invalid", "ip-2"}, {"scoped@authority.invalid", "ip-3"},
	} {
		_, err := login(tc.email, authorityPassword, tc.ip)
		if !errors.Is(err, ErrStaffPortalLogin) {
			t.Fatalf("%s portal login err=%v, want ErrStaffPortalLogin", tc.email, err)
		}
	}
	// 过期的临时提权不算：照常能进门户
	if _, err := login("expired@authority.invalid", authorityPassword, "ip-1"); err != nil {
		t.Fatalf("expired-binding user portal login: %v", err)
	}
	// 口令不对时照旧是「邮箱或密码不正确」：禁登提示只在口令对上之后出现，不能借它枚举管理员
	_, err := login("super@authority.invalid", "wrong-password-1", "ip-2")
	wantHTTPCode(t, "staff wrong password", err, httpx.CodeUnauthorized)
	// 后台域照常登录
	adminSvc := *svc
	adminSvc.issuer = token.NewIssuer("admin", []byte("portal-staff-pg18-admin-32bytes!"), time.Hour)
	if _, err := adminSvc.Login(ctx, f.tenant, LoginInput{
		Email: "super@authority.invalid", Password: authorityPassword, Audience: "admin",
	}); err != nil {
		t.Fatalf("staff admin login: %v", err)
	}
	// 被拦下的尝试没有建出门户会话
	var staffPublic int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FROM sessions WHERE tenant_id=$1 AND user_id IN ($2,$3) AND audience='public'`,
		f.tenant, f.super, f.scoped).Scan(&staffPublic); err != nil {
		t.Fatal(err)
	}
	if staffPublic != 0 {
		t.Fatalf("blocked staff logins created %d public sessions", staffPublic)
	}

	// 快捷登录换会话同样拦：链接是授角色之前在门户会话里签的
	const staffSession = "d5a10000-0000-4000-8000-000000000031"
	if _, err := admin.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, user_id, audience, auth_methods, expires_at)
		VALUES ($1, $2, $3, 'public', ARRAY['password'], now() + interval '1 day')`,
		staffSession, f.tenant, f.super); err != nil {
		t.Fatal(err)
	}
	ql, err := svc.IssueQuickLogin(ctx, f.tenant, f.super, staffSession)
	if err != nil {
		t.Fatalf("issue quick login: %v", err)
	}
	if _, err := svc.ConsumeQuickLogin(ctx, f.tenant, ql.Token, "pg18", nil); !errors.Is(err, ErrStaffPortalLogin) {
		t.Fatalf("staff quick login err=%v, want ErrStaffPortalLogin", err)
	}

	// 失败审计限频：同一 IP 或同一邮箱 10 分钟内只记一条
	for _, a := range []struct{ email, ip string }{
		{"plain@authority.invalid", "ip-2"},  // IP 已记过（super 被拦那次）
		{"plain@authority.invalid", "ip-4"},  // 新 IP、新邮箱：记一条
		{"plain@authority.invalid", "ip-5"},  // 邮箱已记过
		{"nobody@authority.invalid", "ip-6"}, // 不存在的账号：记一条，不挂资源
		{"nobody@authority.invalid", "ip-6"}, // 重复
	} {
		_, err := login(a.email, "wrong-password-2", a.ip)
		wantHTTPCode(t, "wrong password "+a.email, err, httpx.CodeUnauthorized)
	}
	rows, err := admin.Query(ctx, `
		SELECT error_code, coalesce(resource_id::text, ''), actor_kind, actor_id IS NULL
		  FROM audit_events WHERE tenant_id=$1 AND action='user.login_failed'
		 ORDER BY chain_seq`, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	type failRow struct {
		reason, resource, actorKind string
		anonymous                   bool
	}
	var got []failRow
	for rows.Next() {
		var r failRow
		if err := rows.Scan(&r.reason, &r.resource, &r.actorKind, &r.anonymous); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	want := []failRow{
		{loginFailureStaffPortal, f.super, "anonymous", true},
		{loginFailureStaffPortal, f.scoped, "anonymous", true},
		{loginFailureInvalid, f.plain, "anonymous", true},
		{loginFailureInvalid, "", "anonymous", true},
	}
	if len(got) != len(want) {
		t.Fatalf("login failure audit rows=%+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("login failure audit row %d=%+v, want %+v", i, got[i], want[i])
		}
	}
}

// 找回密码：没配邮件就关闭；防枚举；验证码一次性、只认最新一枚、错次数有上限；
// 成功后改密并吊销该账号的全部登录。
func TestPasswordResetPG18(t *testing.T) {
	ctx, admin, app := openLogoutPG18Fixture(t)
	f := seedAuthorityFixture(t, ctx, admin, "d5a20000-0000-4000-8000-", "password-reset-pg18")
	const (
		plainEmail    = "plain@authority.invalid"
		plainSession  = "d5a20000-0000-4000-8000-000000000031"
		plainSession2 = "d5a20000-0000-4000-8000-000000000032"
		newPassword   = "Reset-pass-2026"
	)
	if _, err := admin.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, user_id, audience, auth_methods, expires_at) VALUES
		  ($1, $3, $4, 'public', ARRAY['password'], now() + interval '1 day'),
		  ($2, $3, $4, 'public', ARRAY['password'], now() + interval '1 day')`,
		plainSession, plainSession2, f.tenant, f.plain); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `
		INSERT INTO refresh_tokens (tenant_id, session_id, user_id, token_hash, status, expires_at)
		VALUES ($1, $2, $3, decode('d5a2', 'hex'), 'active', now() + interval '1 day')`,
		f.tenant, plainSession, f.plain); err != nil {
		t.Fatal(err)
	}

	sender := &captureSender{}
	mailer := notify.New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("password-reset-pg18-salt"), sender)
	svc := &Service{
		pool:       app,
		issuer:     token.NewIssuer("public", []byte("password-reset-pg18-secret-32by!"), time.Hour),
		refreshTTL: 24 * time.Hour,
		hashSalt:   []byte("password-reset-pg18-hash"),
	}

	// 没配邮件服务：入口关、接口拒，什么都不写
	svc.SetVerificationMailer(mailOffMailer{mailer})
	if svc.PasswordResetAvailable(ctx, f.tenant) {
		t.Fatal("password reset must be unavailable without mail")
	}
	if _, err := svc.StartPasswordReset(ctx, f.tenant, StartPasswordResetInput{Email: plainEmail}); !errors.Is(err, ErrPasswordResetUnavailable) {
		t.Fatalf("start without mail err=%v", err)
	}
	svc.SetVerificationMailer(mailer)
	if !svc.PasswordResetAvailable(ctx, f.tenant) {
		t.Fatal("password reset must be available with a mail sender")
	}

	codeCount := func() int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM verification_codes WHERE tenant_id=$1 AND purpose='password_reset'`,
			f.tenant).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	start := func(email string) {
		t.Helper()
		out, err := svc.StartPasswordReset(ctx, f.tenant, StartPasswordResetInput{Email: email, UserAgent: "pg18"})
		if err != nil || out.ExpiresAt.IsZero() || out.DevCode != "" {
			t.Fatalf("start %s: out=%+v err=%v", email, out, err)
		}
	}
	// 不存在的邮箱、后台人员：响应一样，但不写验证码、不发信（IAM-006）
	start("nobody@authority.invalid")
	start("super@authority.invalid")
	if n := codeCount(); n != 0 {
		t.Fatalf("unknown or staff email wrote %d reset codes", n)
	}

	lastCode := func() string {
		t.Helper()
		if _, err := mailer.Dispatch(ctx, f.tenant, 10); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if len(sender.sent) == 0 {
			t.Fatal("no reset mail sent")
		}
		m := sender.sent[len(sender.sent)-1]
		code := regexp.MustCompile(`\b\d{6}\b`).FindString(m.body)
		if m.to != plainEmail || code == "" || !regexp.MustCompile(`10 分钟`).MatchString(m.body) {
			t.Fatalf("reset mail to=%q subject=%q body=%q", m.to, m.subject, m.body)
		}
		return code
	}
	start(plainEmail)
	first := lastCode()
	var userID string
	var maxAttempts int
	var ttl time.Duration
	if err := admin.QueryRow(ctx, `
		SELECT user_id::text, max_attempts, expires_at - created_at FROM verification_codes
		 WHERE tenant_id=$1 AND purpose='password_reset' AND target_hash=$2`,
		f.tenant, crypto.HashIdentifier(svc.hashSalt, plainEmail)).Scan(&userID, &maxAttempts, &ttl); err != nil {
		t.Fatal(err)
	}
	if userID != f.plain || maxAttempts != passwordResetMaxAttempts || ttl > 15*time.Minute {
		t.Fatalf("reset code row user=%s max_attempts=%d ttl=%s", userID, maxAttempts, ttl)
	}

	// 再要一次：只认最新一枚，旧的立刻作废
	start(plainEmail)
	second := lastCode()
	complete := func(code, password string) error {
		return svc.CompletePasswordReset(ctx, f.tenant, CompletePasswordResetInput{
			Email: plainEmail, Code: code, NewPassword: password, UserAgent: "pg18",
		})
	}
	invalid := func(label string, err error) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["code"] == "" {
			t.Fatalf("%s: err=%v, want the code validation error", label, err)
		}
	}
	used := 0
	if first != second {
		invalid("superseded code", complete(first, newPassword))
		used++
	}
	// 错满 5 次后，连正确的验证码也不认
	wrong := "000000"
	if second == wrong {
		wrong = "111111"
	}
	for i := used; i < passwordResetMaxAttempts; i++ {
		invalid("wrong code", complete(wrong, newPassword))
	}
	invalid("attempts exhausted", complete(second, newPassword))

	// 重新要一枚，这次填对
	start(plainEmail)
	third := lastCode()
	if err := complete(third, "short1"); err == nil {
		t.Fatal("weak password must be rejected")
	}
	if err := complete(third, newPassword); err != nil {
		t.Fatalf("complete reset: %v", err)
	}
	invalid("code is one-time", complete(third, "Another-pass-2026"))

	// 改密、吊销全部登录、写审计
	var phc string
	var openSessions, activeRefresh, resetAudits, requestAudits int
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT phc FROM user_passwords WHERE user_id=$2),
		       (SELECT count(*) FROM sessions WHERE tenant_id=$1 AND user_id=$2 AND revoked_at IS NULL),
		       (SELECT count(*) FROM refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND status='active'),
		       (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='user.password_reset' AND resource_id=$2),
		       (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='user.password_reset_requested' AND resource_id=$2)`,
		f.tenant, f.plain).Scan(&phc, &openSessions, &activeRefresh, &resetAudits, &requestAudits); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := crypto.VerifyPassword(newPassword, phc); !ok || err != nil {
		t.Fatalf("new password not stored: ok=%v err=%v", ok, err)
	}
	if openSessions != 0 || activeRefresh != 0 || resetAudits != 1 || requestAudits != 3 {
		t.Fatalf("after reset sessions=%d refresh=%d reset_audits=%d request_audits=%d",
			openSessions, activeRefresh, resetAudits, requestAudits)
	}
	var reason string
	if err := admin.QueryRow(ctx, `SELECT revoked_reason FROM sessions WHERE id=$1`, plainSession).Scan(&reason); err != nil || reason != "password_reset" {
		t.Fatalf("session revoke reason=%q err=%v", reason, err)
	}
	if _, err := svc.Login(ctx, f.tenant, LoginInput{Email: plainEmail, Password: authorityPassword}); err == nil {
		t.Fatal("old password still logs in")
	}
	if _, err := svc.Login(ctx, f.tenant, LoginInput{Email: plainEmail, Password: newPassword}); err != nil {
		t.Fatalf("new password login: %v", err)
	}
}
