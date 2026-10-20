package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// adminLogin 后台登录并返回令牌（需事先 addAdmin）。
func adminLogin(t *testing.T, h *testHarness, ctx context.Context, email, pass string) Tokens {
	t.Helper()
	tok, err := h.S.Login(ctx, authn.AudienceAdmin, email, pass)
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	return tok
}

func TestRefreshRotateGraceReuse(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("ref@example.com", "pass", UserStatusActive)

	tok, err := h.S.Login(ctx, authn.AudiencePublic, "ref@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	refresh1 := tok.Refresh

	tok2, err := h.S.Refresh(ctx, authn.AudiencePublic, refresh1)
	if err != nil {
		t.Fatalf("首次 refresh: %v", err)
	}
	if tok2.Refresh == "" {
		t.Fatal("轮换后应下发新 refresh")
	}
	refresh2 := tok2.Refresh

	tok3, err := h.S.Refresh(ctx, authn.AudiencePublic, refresh1)
	if err != nil {
		t.Fatalf("宽限期内用旧 refresh: %v", err)
	}
	if tok3.Refresh == "" || tok3.Refresh == refresh2 {
		t.Fatal("宽限期也应补发一枚新 refresh")
	}
	if tok3.AccessToken == "" {
		t.Fatal("宽限期应发 access")
	}

	// 补发不延长宽限：从第一次轮换算起过了 30 秒，旧的那枚照旧判重放
	h.Clock.Advance(31 * time.Second)
	_, err = h.S.Refresh(ctx, authn.AudiencePublic, refresh1)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("宽限外旧 refresh 应吊销会话: %v", err)
	}
	reuseAudit := 0
	for _, r := range h.Audit.events {
		if r.ev.Action == "session.refresh_reuse" {
			reuseAudit++
			// anonymous：不带 ActorID/Audience；被影响的用户放 ResourceID，audience 放 Data
			if r.ev.ActorID != nil || r.ev.Audience != "" || r.ev.ResourceType != "user" ||
				r.ev.ResourceID == nil || *r.ev.ResourceID != tok.UserID || r.ev.Data["audience"] != authn.AudiencePublic {
				t.Fatalf("refresh_reuse 审计字段: %+v", r.ev)
			}
		}
	}
	if reuseAudit != 1 {
		t.Fatalf("refresh_reuse 审计=%d want 1", reuseAudit)
	}

	_, err = h.S.Refresh(ctx, authn.AudiencePublic, refresh2)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("会话已吊销后 refresh2 也应失败: %v", err)
	}
}

// 轮换的响应没送到浏览器（关页、F5、断网）：浏览器仍拿着上一枚，宽限内再来一次就拿回有效的 cookie，过了宽限照常能用。
func TestRefreshLostResponseRecovers(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("lost@example.com", "pass", UserStatusActive)
	tok, err := h.S.Login(ctx, authn.AudiencePublic, "lost@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh); err != nil {
		t.Fatalf("首次 refresh（响应丢了）: %v", err)
	}
	h.Clock.Advance(10 * time.Second)
	again, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh)
	if err != nil || again.Refresh == "" {
		t.Fatalf("宽限内用上一枚应补发新 refresh: %+v %v", again, err)
	}
	h.Clock.Advance(5 * time.Minute)
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, again.Refresh); err != nil {
		t.Fatalf("补发的那枚过了宽限也应照常可用: %v", err)
	}
	for _, r := range h.Audit.events {
		if r.ev.Action == "session.refresh_reuse" {
			t.Fatal("响应丢失不应记成重放")
		}
	}
}

// 宽限内补发后，被换下的那一枚（第一次轮换发出的）又出现：上一枚落在了两个持有人手里，判重放、吊销整条会话。
func TestRefreshReplacedInGraceIsReuse(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("twice@example.com", "pass", UserStatusActive)
	tok, err := h.S.Login(ctx, authn.AudiencePublic, "twice@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	second, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh)
	if err != nil {
		t.Fatalf("首次 refresh: %v", err)
	}
	third, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh)
	if err != nil {
		t.Fatalf("宽限内用上一枚: %v", err)
	}
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, second.Refresh); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("被换下的那一枚再出现应判重放: %v", err)
	}
	reuse := 0
	for _, r := range h.Audit.events {
		if r.ev.Action == "session.refresh_reuse" {
			reuse++
		}
	}
	if reuse != 1 {
		t.Fatalf("refresh_reuse 审计=%d want 1", reuse)
	}
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, third.Refresh); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("会话已吊销，补发的那枚也应失败: %v", err)
	}
}

func TestAdminRefreshRechecksAdmin(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	a := h.Users.addUser("keep@example.com", "pass", UserStatusActive)
	b := h.Users.addUser("gone@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(a)
	h.Store.addAdmin(b)

	tokA := adminLogin(t, h, ctx, "keep@example.com", "pass")
	tokB := adminLogin(t, h, ctx, "gone@example.com", "pass")
	if _, err := h.S.Refresh(ctx, authn.AudienceAdmin, tokB.Refresh); err != nil {
		t.Fatalf("仍是管理员时 refresh: %v", err)
	}

	// 绕过 RevokeAdmin 直接移出名单，模拟会话吊销漏掉的情形：refresh 也要拒绝并删掉会话
	delete(h.Store.admins, b)
	if _, err := h.S.Refresh(ctx, authn.AudienceAdmin, tokB.Refresh); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("已不是管理员的 refresh 应 401: %v", err)
	}
	if _, err := h.S.Authenticate(ctx, authn.AudienceAdmin, tokB.AccessToken); err == nil {
		t.Fatal("refresh 被拒后会话应已删除")
	}

	// 账号不再 active 同理
	u := h.Users.users[a]
	u.Status = "suspended"
	h.Users.users[a] = u
	if _, err := h.S.Refresh(ctx, authn.AudienceAdmin, tokA.Refresh); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("停用账号的 refresh 应 401: %v", err)
	}
}

func TestRefreshWrongAudience(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("adm@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(uid)

	tok := adminLogin(t, h, ctx, "adm@example.com", "pass")
	_, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("跨域 refresh 应 401: %v", err)
	}
}

func TestLogout(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("out@example.com", "pass", UserStatusActive)

	tok, err := h.S.Login(ctx, authn.AudiencePublic, "out@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	p, err := h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	pctx := authn.With(ctx, p)
	if err := h.S.Logout(pctx, authn.AudiencePublic, tok.Refresh); err != nil {
		t.Fatalf("logout: %v", err)
	}
	_, err = h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("登出后 access 应失效: %v", err)
	}
	if err := h.S.Logout(pctx, authn.AudiencePublic, tok.Refresh); err != nil {
		t.Fatalf("二次 logout 应幂等: %v", err)
	}
	logoutN := 0
	for _, r := range h.Audit.events {
		if r.ev.Action == "user.logout" {
			logoutN++
		}
	}
	if logoutN != 1 {
		t.Fatalf("user.logout 审计=%d want 1", logoutN)
	}
}

func TestAdminIdleTimeout(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("idle@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(uid)

	tok := adminLogin(t, h, ctx, "idle@example.com", "pass")
	h.Clock.Advance(2*time.Hour + time.Second)

	_, err := h.S.Authenticate(ctx, authn.AudienceAdmin, tok.AccessToken)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("空闲超时后 authenticate 应失败: %v", err)
	}
	_, err = h.S.Refresh(ctx, authn.AudienceAdmin, tok.Refresh)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("空闲超时后 refresh 应失败: %v", err)
	}
}

func TestRevokeUserKeepsCurrent(t *testing.T) {
	h := newTestService(t)
	email := "multi@example.com"
	h.Users.addUser(email, "pass", UserStatusActive)

	ctx1 := testCtx("203.0.113.11")
	ctx2 := testCtx("203.0.113.12")
	tok1, err := h.S.Login(ctx1, authn.AudiencePublic, email, "pass")
	if err != nil {
		t.Fatalf("login1: %v", err)
	}
	tok2, err := h.S.Login(ctx2, authn.AudiencePublic, email, "pass")
	if err != nil {
		t.Fatalf("login2: %v", err)
	}
	p1, err := h.S.Authenticate(ctx1, authn.AudiencePublic, tok1.AccessToken)
	if err != nil {
		t.Fatalf("auth1: %v", err)
	}
	if err := h.S.RevokeUser(ctx1, p1.UserID, p1.SessionID); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}
	_, err = h.S.Authenticate(ctx1, authn.AudiencePublic, tok1.AccessToken)
	if err != nil {
		t.Fatalf("保留会话应仍有效: %v", err)
	}
	_, err = h.S.Authenticate(ctx2, authn.AudiencePublic, tok2.AccessToken)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("另一会话应失效: %v", err)
	}
}

func TestReauth(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("re@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(uid)

	tok := adminLogin(t, h, ctx, "re@example.com", "pass")
	p, err := h.S.Authenticate(ctx, authn.AudienceAdmin, tok.AccessToken)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	actx := authn.With(ctx, p)

	_, err = h.S.Reauth(actx, "wrong")
	if errs.As(err).Code != errs.Unauthorized {
		t.Fatalf("错口令: %v", err)
	}
	failAudit := 0
	for _, r := range h.Audit.events {
		if r.ev.Action == "user.reauthenticated" && r.ev.Outcome == "failure" {
			failAudit++
		}
	}
	if failAudit != 1 {
		t.Fatalf("重认证失败审计=%d want 1", failAudit)
	}

	t2, err := h.S.Reauth(actx, "pass")
	if err != nil {
		t.Fatalf("reauth ok: %v", err)
	}
	if t2.Refresh != "" {
		t.Fatal("reauth 不应换 refresh")
	}
	p2, err := h.S.Authenticate(ctx, authn.AudienceAdmin, t2.AccessToken)
	if err != nil {
		t.Fatalf("新 access: %v", err)
	}
	if !p2.RecentlyReauthed(h.Clock.Now()) {
		t.Fatal("重认证后应在窗口内")
	}
	prof, err := h.S.AdminProfile(authn.With(ctx, p2))
	if err != nil || !prof.Reauthed {
		t.Fatalf("AdminProfile Reauthed=%v err=%v", prof.Reauthed, err)
	}

	h.Clock.Advance(authn.ReauthWindow + time.Second)
	prof2, err := h.S.AdminProfile(authn.With(ctx, p2))
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if prof2.Reauthed {
		t.Fatal("超过窗口后 Reauthed 应为 false")
	}
}

func TestListAndRevokeSession(t *testing.T) {
	h := newTestService(t)
	email := "lst@example.com"
	h.Users.addUser(email, "pass", UserStatusActive)

	ctxA := testCtx("203.0.113.21")
	ctxB := testCtx("203.0.113.22")
	tokA, _ := h.S.Login(ctxA, authn.AudiencePublic, email, "pass")
	tokB, _ := h.S.Login(ctxB, authn.AudiencePublic, email, "pass")
	pA, _ := h.S.Authenticate(ctxA, authn.AudiencePublic, tokA.AccessToken)
	pB, _ := h.S.Authenticate(ctxB, authn.AudiencePublic, tokB.AccessToken)

	list, err := h.S.ListSessions(authn.With(ctxA, pA))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("会话数=%d want 2", len(list))
	}
	var currentN int
	for _, s := range list {
		if s.Current {
			currentN++
		}
	}
	if currentN != 1 {
		t.Fatalf("Current 标记=%d want 1", currentN)
	}

	if err := h.S.RevokeSession(authn.With(ctxA, pA), pB.SessionID); err != nil {
		t.Fatalf("revoke other: %v", err)
	}
	_, err = h.S.Authenticate(ctxB, authn.AudiencePublic, tokB.AccessToken)
	if !errors.Is(err, errSessionInvalid) {
		t.Fatalf("被踢会话应失效: %v", err)
	}

	err = h.S.RevokeSession(authn.With(ctxA, pA), pA.SessionID)
	if errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("踢当前会话应校验错误: %v", err)
	}
}

func TestReauthAttemptLimit(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("lim@example.com", "pass", UserStatusActive)
	h.Store.addAdmin(uid)
	tok := adminLogin(t, h, ctx, "lim@example.com", "pass")
	p, err := h.S.Authenticate(ctx, authn.AudienceAdmin, tok.AccessToken)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	actx := authn.With(ctx, p)

	// 错 4 次后对一次：计数清零
	for i := 0; i < reauthLimit-1; i++ {
		if _, err := h.S.Reauth(actx, "wrong"); errs.As(err).Code != errs.Unauthorized {
			t.Fatalf("第 %d 次错口令: %v", i+1, err)
		}
	}
	if _, err := h.S.Reauth(actx, "pass"); err != nil {
		t.Fatalf("对口令应清零计数: %v", err)
	}
	// 再错满 5 次：第 6 次即使口令正确也 429，带 Retry-After
	for i := 0; i < reauthLimit; i++ {
		if _, err := h.S.Reauth(actx, "wrong"); errs.As(err).Code != errs.Unauthorized {
			t.Fatalf("第 %d 次错口令: %v", i+1, err)
		}
	}
	_, err = h.S.Reauth(actx, "pass")
	e := errs.As(err)
	if e.Code != errs.RateLimited || e.RetryAfterSeconds <= 0 {
		t.Fatalf("超限应 429 带 Retry-After: code=%v retry=%d", e.Code, e.RetryAfterSeconds)
	}
	// 窗口过后恢复
	h.Redis.FastForward(reauthWindow + time.Second)
	if _, err := h.S.Reauth(actx, "pass"); err != nil {
		t.Fatalf("窗口过后应恢复: %v", err)
	}
}

func TestRefreshRandomSecretDoesNotRevoke(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("rnd@example.com", "pass", UserStatusActive)
	tok, err := h.S.Login(ctx, authn.AudiencePublic, "rnd@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	sid, _, ok := parseRefresh(tok.Refresh)
	if !ok {
		t.Fatal("refresh 格式")
	}
	// 只知道 sid（不保密）配一个随机值：401，但不吊销、不写重放审计
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, formatRefresh(sid, "random-guess")); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("随机值应 401: %v", err)
	}
	for _, r := range h.Audit.events {
		if r.ev.Action == "session.refresh_reuse" {
			t.Fatal("随机值不应写重放审计")
		}
	}
	if _, err := h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken); err != nil {
		t.Fatalf("会话应仍有效: %v", err)
	}
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh); err != nil {
		t.Fatalf("真 refresh 应仍可用: %v", err)
	}
}

func TestAuthenticateUsesSessionAudience(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	uid := h.Users.addUser("aud@example.com", "pass", UserStatusActive)
	now := h.Clock.Now()

	// 门户会话，却拿后台签发器签了一枚 access（模拟密钥配错之类的纵深场景）
	rec, _, err := h.S.newSession(ctx, uid, authn.AudiencePublic, []string{amrPassword}, "0", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.S.sessions.create(ctx, rec, now); err != nil || !ok {
		t.Fatalf("create: ok=%v err=%v", ok, err)
	}
	access, _, err := h.S.issueAccess(authn.AudienceAdmin, uid, rec.ID, rec.AMR, now)
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.S.Authenticate(ctx, authn.AudienceAdmin, access)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p.Audience != authn.AudiencePublic {
		t.Fatalf("主体的域应取会话自己的域，得 %q", p.Audience)
	}
}
