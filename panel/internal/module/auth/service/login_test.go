package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/auth/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestLoginPublicSuccess(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("user@example.com", "secret-pass", UserStatusActive)

	tok, err := h.S.Login(ctx, authn.AudiencePublic, "user@example.com", "secret-pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if tok.AccessToken == "" {
		t.Fatal("AccessToken 为空")
	}
	if tok.Refresh == "" || !strings.Contains(tok.Refresh, ".") {
		t.Fatalf("Refresh 格式不对: %q", tok.Refresh)
	}
	wantExp := h.Clock.Now().Add(720 * time.Hour)
	if !tok.RefreshExpires.Equal(wantExp) {
		t.Fatalf("RefreshExpires=%v want %v", tok.RefreshExpires, wantExp)
	}

	p, err := h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p.UserID != uid {
		t.Fatalf("UserID=%v want %v", p.UserID, uid)
	}
	if p.SessionID == "" {
		t.Fatal("SessionID 为空")
	}

	ev := loginSuccessEvents(h.Audit)
	if len(ev) != 1 {
		t.Fatalf("user.login 条数=%d want 1", len(ev))
	}
	if !ev[0].hasP || ev[0].p.Audience != authn.AudiencePublic || ev[0].p.UserID != uid {
		t.Fatalf("登录审计主体: hasP=%v p=%+v", ev[0].hasP, ev[0].p)
	}
}

func TestLoginIAM006(t *testing.T) {
	// 失败审计按邮箱与 IP 各 10 分钟去重：每次换 IP，并把时钟拨过去重窗口。
	h := newTestService(t)
	h.Users.addUser("admin@example.com", "right-pass", UserStatusActive)
	h.Users.addUser("off@example.com", "right-pass", "disabled")

	step := func(ip, email, pass string) error {
		_, err := h.S.Login(testCtx(ip), authn.AudienceAdmin, email, pass)
		h.Redis.FastForward(failureAuditWindow + time.Second)
		return err
	}
	errNoRole := step("203.0.113.1", "admin@example.com", "right-pass")
	errBadPass := step("203.0.113.2", "admin@example.com", "wrong-pass")
	errMissing := step("203.0.113.3", "nobody@example.com", "any")
	errInactive := step("203.0.113.4", "off@example.com", "right-pass")

	for name, err := range map[string]error{
		"no_admin_role": errNoRole,
		"bad_password":  errBadPass,
		"no_user":       errMissing,
		"inactive":      errInactive,
	} {
		if !errors.Is(err, errInvalidCredentials) {
			t.Fatalf("%s: 应 Is errInvalidCredentials, 得 %v", name, err)
		}
		e := errs.As(err)
		if e.Code != errs.Unauthorized || e.Message != "邮箱或密码不正确" {
			t.Fatalf("%s: code/msg=%v/%q", name, e.Code, e.Message)
		}
	}

	failed := loginFailedEvents(h.Audit)
	if len(failed) != 4 {
		t.Fatalf("login_failed 条数=%d want 4", len(failed))
	}
	reasons := map[string]int{}
	for _, r := range failed {
		reason, _ := r.ev.Data["reason"].(string)
		reasons[reason]++
		// anonymous：不带 ActorID 与 Audience，audience 在 Data 里
		if r.ev.ActorID != nil || r.ev.Audience != "" || r.ev.Data["audience"] != authn.AudienceAdmin {
			t.Fatalf("失败审计应 anonymous: %+v", r.ev)
		}
	}
	if reasons[reasonNoAdminRole] != 1 || reasons[reasonInvalidCredentials] != 2 || reasons[reasonAccountInactive] != 1 {
		t.Fatalf("reason 统计=%v", reasons)
	}
	// 账号存在的失败把被撞账号放 ResourceID，不存在的不放
	withResource := 0
	for _, r := range failed {
		if r.ev.ResourceID != nil {
			if r.ev.ResourceType != "user" {
				t.Fatalf("ResourceType=%q", r.ev.ResourceType)
			}
			withResource++
		}
	}
	if withResource != 3 {
		t.Fatalf("带 ResourceID 的失败审计=%d want 3", withResource)
	}
}

func TestLoginAdminPermissions(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("admin@example.com", "pass", UserStatusActive)
	h.Store.perms[uid] = []string{"iam.user.read", "catalog.read"}

	tok, err := h.S.Login(ctx, authn.AudienceAdmin, "admin@example.com", "pass")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	if len(tok.Permissions) != 2 || tok.Permissions[0] != "iam.user.read" {
		t.Fatalf("Permissions=%v", tok.Permissions)
	}

	_, err = h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("跨域 public authenticate 应 401, 得 %v", err)
	}
}

func TestLoginLockout(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	email := "lock@example.com"
	h.Users.addUser(email, "good", UserStatusActive)

	for i := 0; i < failureThreshold; i++ {
		_, err := h.S.Login(ctx, authn.AudiencePublic, email, "bad")
		if !errors.Is(err, errInvalidCredentials) {
			t.Fatalf("第 %d 次应 invalid creds: %v", i+1, err)
		}
	}
	_, err := h.S.Login(ctx, authn.AudiencePublic, email, "good")
	e := errs.As(err)
	if e.Code != errs.RateLimited {
		t.Fatalf("第 11 次 code=%v want RateLimited", e.Code)
	}
	if e.RetryAfterSeconds <= 0 {
		t.Fatalf("RetryAfterSeconds=%d", e.RetryAfterSeconds)
	}

	ctx2 := testCtx("203.0.113.8")
	ghost := "ghost@example.com"
	for i := 0; i < failureThreshold; i++ {
		_, _ = h.S.Login(ctx2, authn.AudiencePublic, ghost, "bad")
	}
	_, errGhost := h.S.Login(ctx2, authn.AudiencePublic, ghost, "anything")
	if errs.As(errGhost).Code != errs.RateLimited {
		t.Fatalf("不存在邮箱满 10 次也应暂停: %v", errGhost)
	}
}

func TestLoginStaffPortalBlocked(t *testing.T) {
	h := newTestService(t)
	uid := h.Users.addUser("staff@example.com", "pass", UserStatusActive)
	h.Store.roles[uid] = []repo.RoleRow{{ID: uuid.Must(uuid.NewV7()), Code: "ops", Name: "运维"}}

	// 口令是对的：不占失败名额，超过阈值次数也不会被暂停
	for i := 0; i < failureThreshold+2; i++ {
		ctx := testCtx(fmt.Sprintf("203.0.113.%d", i+10))
		_, err := h.S.Login(ctx, authn.AudiencePublic, "staff@example.com", "pass")
		if !errors.Is(err, ErrStaffPortalLogin) {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
		if errs.As(err).Code != errs.Forbidden {
			t.Fatalf("应为 Forbidden")
		}
	}
	blocked := 0
	for _, r := range h.Audit.events {
		if r.ev.Action == loginFailedAction && r.ev.Data["reason"] == reasonStaffPortal {
			blocked++
		}
	}
	if blocked != failureThreshold+2 {
		t.Fatalf("staff_portal_blocked 审计=%d want %d", blocked, failureThreshold+2)
	}
	if n, _ := h.RDB.Get(testCtx("203.0.113.1"), failureKey(uid, "")).Int(); n != 0 {
		t.Fatalf("后台人员登门户不应留下失败计数，得 %d", n)
	}
}

func TestLoginEpochRace(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("race@example.com", "pass", UserStatusActive)
	h.Users.onAuth = func() {
		_ = h.RDB.Incr(ctx, epochKey(uid)).Err()
	}

	_, err := h.S.Login(ctx, authn.AudiencePublic, "race@example.com", "pass")
	if !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("纪元竞态应 401: %v", err)
	}
	if n := userSessCount(ctx, h, uid); n != 0 {
		t.Fatalf("usess 应为空, 得 %d", n)
	}
}

func TestLoginFailureAuditDedup(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.99")
	for i := 0; i < 5; i++ {
		email := "missing" + string(rune('a'+i)) + "@example.com"
		_, _ = h.S.Login(ctx, authn.AudiencePublic, email, "x")
	}
	failed := loginFailedEvents(h.Audit)
	if len(failed) != 1 {
		t.Fatalf("同 IP 5 次不存在邮箱应只记 1 条失败审计, 得 %d", len(failed))
	}
}

func TestQuickLogin(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("q@example.com", "pass", UserStatusActive)

	tok, err := h.S.Login(ctx, authn.AudiencePublic, "q@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	p, err := h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}

	link, err := h.S.IssueQuickLogin(authn.With(ctx, p))
	if err != nil {
		t.Fatalf("issue quick: %v", err)
	}
	if link.Token == "" || link.ExpiresIn != 60 {
		t.Fatalf("link=%+v", link)
	}

	t2, err := h.S.ConsumeQuickLogin(ctx, link.Token)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if t2.UserID != uid {
		t.Fatalf("uid=%v", t2.UserID)
	}
	p2, err := h.S.Authenticate(ctx, authn.AudiencePublic, t2.AccessToken)
	if err != nil {
		t.Fatalf("auth new: %v", err)
	}
	if p2.SessionID == p.SessionID {
		t.Fatal("快捷登录应新会话")
	}
	if !hasAMR(p2.AMR, amrQuickLogin) {
		t.Fatalf("AMR=%v", p2.AMR)
	}

	_, err = h.S.ConsumeQuickLogin(ctx, link.Token)
	if !errors.Is(err, errQuickLoginInvalid) {
		t.Fatalf("二次消费: %v", err)
	}

	// 另一门户会话：先签发快捷链接，再登出源会话，消费应失败。
	h2 := newTestService(t)
	ctxB := testCtx("203.0.113.20")
	h2.Users.addUser("q2@example.com", "pass", UserStatusActive)
	tokB, err := h2.S.Login(ctxB, authn.AudiencePublic, "q2@example.com", "pass")
	if err != nil {
		t.Fatalf("login2: %v", err)
	}
	pB, _ := h2.S.Authenticate(ctxB, authn.AudiencePublic, tokB.AccessToken)
	linkB, err := h2.S.IssueQuickLogin(authn.With(ctxB, pB))
	if err != nil {
		t.Fatalf("issue before logout: %v", err)
	}
	if err := h2.S.Logout(authn.With(ctxB, pB), authn.AudiencePublic, tokB.Refresh); err != nil {
		t.Fatalf("logout: %v", err)
	}
	_, err = h2.S.ConsumeQuickLogin(ctxB, linkB.Token)
	if errs.As(err).Code != errs.Unauthorized {
		t.Fatalf("源会话登出后消费应 401, 得 %v", err)
	}
}

func TestLoginLockoutSharedAcrossDomains(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("both@example.com", "good", UserStatusActive)
	h.Store.perms[uid] = []string{PermIAMUserRead}

	for i := 0; i < failureThreshold; i++ {
		_, _ = h.S.Login(ctx, authn.AudiencePublic, "both@example.com", "bad")
	}
	// 同一账号在后台入口也被暂停（计数不分域）
	_, err := h.S.Login(ctx, authn.AudienceAdmin, "both@example.com", "good")
	if e := errs.As(err); e.Code != errs.RateLimited || e.RetryAfterSeconds <= 0 {
		t.Fatalf("跨域应同样暂停: %v", err)
	}
	// 暂停期过后恢复
	h.Redis.FastForward(failurePause + time.Second)
	if _, err := h.S.Login(ctx, authn.AudienceAdmin, "both@example.com", "good"); err != nil {
		t.Fatalf("暂停期后应可登录: %v", err)
	}
}

func TestLoginSuccessClearsAndTouches(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("ok@example.com", "good", UserStatusActive)

	for i := 0; i < failureThreshold-1; i++ {
		_, _ = h.S.Login(ctx, authn.AudiencePublic, "ok@example.com", "bad")
	}
	if _, err := h.S.Login(ctx, authn.AudiencePublic, "ok@example.com", "good"); err != nil {
		t.Fatalf("第 10 次口令对应成功: %v", err)
	}
	if h.Redis.Exists(failureKey(uid, "")) {
		t.Fatal("成功后应清掉失败计数")
	}
	if len(h.Users.touched) != 1 || h.Users.touched[0] != uid {
		t.Fatalf("成功后应记最近登录时间: %v", h.Users.touched)
	}
}

func TestLoginBackendErrorReleasesAttempt(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("busy@example.com", "good", UserStatusActive)
	h.Users.authErr = errs.Newf(errs.Unavailable, "口令校验繁忙，请稍后重试")

	for i := 0; i < failureThreshold+2; i++ {
		_, err := h.S.Login(ctx, authn.AudiencePublic, "busy@example.com", "good")
		if errs.As(err).Code != errs.Unavailable {
			t.Fatalf("第 %d 次应透传 503: %v", i+1, err)
		}
	}
	if n, _ := h.RDB.Get(ctx, failureKey(uid, "")).Int(); n != 0 {
		t.Fatalf("没有结论的结果应退回名额，计数=%d", n)
	}
}
