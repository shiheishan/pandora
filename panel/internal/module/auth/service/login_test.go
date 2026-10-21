package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
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
	// admin@example.com 口令对、但不在管理员名单里
	errNotAdmin := step("203.0.113.1", "admin@example.com", "right-pass")
	errBadPass := step("203.0.113.2", "admin@example.com", "wrong-pass")
	errMissing := step("203.0.113.3", "nobody@example.com", "any")
	errInactive := step("203.0.113.4", "off@example.com", "right-pass")

	for name, err := range map[string]error{
		"not_admin":    errNotAdmin,
		"bad_password": errBadPass,
		"no_user":      errMissing,
		"inactive":     errInactive,
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
	if reasons[reasonNotAdmin] != 1 || reasons[reasonInvalidCredentials] != 2 || reasons[reasonAccountInactive] != 1 {
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

func TestLoginAdmin(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("admin@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(uid)

	tok, err := h.S.Login(ctx, authn.AudienceAdmin, "admin@example.com", "pass")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	if tok.UserID != uid || tok.AccessToken == "" {
		t.Fatalf("tokens=%+v", tok)
	}

	_, err = h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("跨域 public authenticate 应 401, 得 %v", err)
	}
}

// 非管理员口令对也算一次失败：满 10 次后暂停，口令对也 429。
func TestLoginNotAdminCountsFailure(t *testing.T) {
	h := newTestService(t)
	uid := h.Users.addUser("plain@example.com", "pass", UserStatusActive)
	for i := 0; i < failureThreshold; i++ {
		_, err := h.S.Login(testCtx("203.0.113.9"), authn.AudienceAdmin, "plain@example.com", "pass")
		if !errors.Is(err, errInvalidCredentials) {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
	}
	if n, _ := h.RDB.Get(testCtx("203.0.113.9"), failureKey(uid, "")).Int(); n != failureThreshold {
		t.Fatalf("失败计数=%d want %d", n, failureThreshold)
	}
	h.Store.addAdmin(uid)
	_, err := h.S.Login(testCtx("203.0.113.9"), authn.AudienceAdmin, "plain@example.com", "pass")
	if errs.As(err).Code != errs.RateLimited {
		t.Fatalf("暂停期内应 429: %v", err)
	}
	// 运维 reset-password 后清掉计数，立刻能登
	if err := h.S.ClearLoginFailures(testCtx("203.0.113.9"), uid); err != nil {
		t.Fatalf("ClearLoginFailures: %v", err)
	}
	if _, err := h.S.Login(testCtx("203.0.113.9"), authn.AudienceAdmin, "plain@example.com", "pass"); err != nil {
		t.Fatalf("清计数后应可登录: %v", err)
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
	h.Store.addAdmin(uid)

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

// 签发后账号被停用：消费时回「账号已停用」，不建会话（快捷登录不绑凭据纪元，60 秒内能走到这一支）。
func TestQuickLoginDisabledAccount(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("qd@example.com", "pass", UserStatusActive)
	tok, err := h.S.Login(ctx, authn.AudiencePublic, "qd@example.com", "pass")
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

	h.Users.mu.Lock()
	u := h.Users.users[uid]
	u.Status = "suspended"
	h.Users.users[uid] = u
	h.Users.mu.Unlock()

	got, err := h.S.ConsumeQuickLogin(ctx, link.Token)
	if !errors.Is(err, errAccountDisabled) {
		t.Fatalf("停用后消费应回账号已停用：%v", err)
	}
	if got.AccessToken != "" {
		t.Fatal("停用账号不应拿到令牌")
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
	h.Store.addAdmin(uid)

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

func TestLoginDisabledPublic(t *testing.T) {
	h := newTestService(t)
	_ = h.Users.addUser("off@example.com", "right-pass", "suspended")

	// 门户登录口令对 → errors.Is(err, errAccountDisabled)，errs.As(err).Code == errs.Forbidden，message 「账号已停用」
	tok1, err1 := h.S.Login(testCtx("203.0.113.1"), authn.AudiencePublic, "off@example.com", "right-pass")
	if !errors.Is(err1, errAccountDisabled) {
		t.Fatalf("口令对应 Is errAccountDisabled, 得 %v", err1)
	}
	if e := errs.As(err1); e.Code != errs.Forbidden || e.Message != "账号已停用" {
		t.Fatalf("code/msg=%v/%q, want forbidden/账号已停用", e.Code, e.Message)
	}
	if tok1.AccessToken != "" {
		t.Fatalf("不应返回 AccessToken: %+v", tok1)
	}

	h.Redis.FastForward(failureAuditWindow + time.Second)

	// 口令错 → errors.Is(err, errInvalidCredentials)
	tok2, err2 := h.S.Login(testCtx("203.0.113.2"), authn.AudiencePublic, "off@example.com", "wrong-pass")
	if !errors.Is(err2, errInvalidCredentials) {
		t.Fatalf("口令错应 Is errInvalidCredentials, 得 %v", err2)
	}
	if e := errs.As(err2); e.Code != errs.Unauthorized || e.Message != "邮箱或密码不正确" {
		t.Fatalf("code/msg=%v/%q, want unauthorized/邮箱或密码不正确", e.Code, e.Message)
	}
	if tok2.AccessToken != "" {
		t.Fatalf("不应返回 AccessToken: %+v", tok2)
	}

	// 两次都不建会话（loginSuccessEvents(h.Audit) 为 0 条）
	if success := loginSuccessEvents(h.Audit); len(success) != 0 {
		t.Fatalf("不应建会话，loginSuccessEvents=%d", len(success))
	}

	// 失败审计 reason 分别是 account_inactive 与 invalid_credentials
	failed := loginFailedEvents(h.Audit)
	if len(failed) != 2 {
		t.Fatalf("loginFailedEvents=%d want 2", len(failed))
	}
	if r, _ := failed[0].ev.Data["reason"].(string); r != reasonAccountInactive {
		t.Fatalf("第 1 次失败 reason=%q want %q", r, reasonAccountInactive)
	}
	if r, _ := failed[1].ev.Data["reason"].(string); r != reasonInvalidCredentials {
		t.Fatalf("第 2 次失败 reason=%q want %q", r, reasonInvalidCredentials)
	}

	// 再用 "banned" 状态的另一个账号测一次口令对 → errAccountDisabled
	_ = h.Users.addUser("banned@example.com", "right-pass", "banned")
	tok3, err3 := h.S.Login(testCtx("203.0.113.3"), authn.AudiencePublic, "banned@example.com", "right-pass")
	if !errors.Is(err3, errAccountDisabled) {
		t.Fatalf("banned 口令对应 Is errAccountDisabled, 得 %v", err3)
	}
	if tok3.AccessToken != "" {
		t.Fatalf("不应返回 AccessToken: %+v", tok3)
	}
}

func TestLoginDisabledCountsAsFailure(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	_ = h.Users.addUser("off@example.com", "right-pass", "suspended")

	for i := 0; i < failureThreshold; i++ {
		_, err := h.S.Login(ctx, authn.AudiencePublic, "off@example.com", "right-pass")
		if !errors.Is(err, errAccountDisabled) {
			t.Fatalf("第 %d 次应 Is errAccountDisabled, 得 %v", i+1, err)
		}
	}
	// 第 11 次回 errs.RateLimited（计数照旧，口令对也算一次失败）
	_, err := h.S.Login(ctx, authn.AudiencePublic, "off@example.com", "right-pass")
	if e := errs.As(err); e.Code != errs.RateLimited || e.RetryAfterSeconds <= 0 {
		t.Fatalf("第 11 次应 RateLimited: %v", err)
	}
}

func TestLoginDisabledAdminAudience(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")

	adminUID := h.Users.addUser("admin-off@example.com", "right-pass", "suspended")
	h.Store.addAdmin(adminUID)

	_ = h.Users.addUser("user-off@example.com", "right-pass", "suspended")

	// 一个停用的管理员（addUser + h.Store.addAdmin）后台登录口令对 → errAccountDisabled
	tokAdmin, errAdmin := h.S.Login(ctx, authn.AudienceAdmin, "admin-off@example.com", "right-pass")
	if !errors.Is(errAdmin, errAccountDisabled) {
		t.Fatalf("停用管理员后台登录口令对应 Is errAccountDisabled, 得 %v", errAdmin)
	}
	if e := errs.As(errAdmin); e.Code != errs.Forbidden || e.Message != "账号已停用" {
		t.Fatalf("code/msg=%v/%q want forbidden/账号已停用", e.Code, e.Message)
	}
	if tokAdmin.AccessToken != "" {
		t.Fatalf("不应返回 AccessToken: %+v", tokAdmin)
	}

	// 一个停用的非管理员后台登录口令对 → errInvalidCredentials（文案「邮箱或密码不正确」）
	tokPlain, errPlain := h.S.Login(ctx, authn.AudienceAdmin, "user-off@example.com", "right-pass")
	if !errors.Is(errPlain, errInvalidCredentials) {
		t.Fatalf("停用非管理员后台登录口令对应 Is errInvalidCredentials, 得 %v", errPlain)
	}
	if e := errs.As(errPlain); e.Code != errs.Unauthorized || e.Message != "邮箱或密码不正确" {
		t.Fatalf("code/msg=%v/%q want unauthorized/邮箱或密码不正确", e.Code, e.Message)
	}
	if tokPlain.AccessToken != "" {
		t.Fatalf("不应返回 AccessToken: %+v", tokPlain)
	}
}
