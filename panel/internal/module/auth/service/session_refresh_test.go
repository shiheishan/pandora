package service

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/authn"
)

func countAudit(h *testHarness, action string) int {
	n := 0
	for _, r := range h.Audit.events {
		if r.ev.Action == action {
			n++
		}
	}
	return n
}

// 只有主密钥与 sid（不保密）、没有这条会话的种子：算出的「第 0 代」不被认，只回 401，不吊销、不写重放审计；
// 同一 sid 同一代，种子不同算出的值也不同。
func TestRefreshSeedBindsValue(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("seed@example.com", "pass", UserStatusActive)
	tok, err := h.S.Login(ctx, authn.AudiencePublic, "seed@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	sid, _, _, ok := parseRefresh(tok.Refresh)
	if !ok {
		t.Fatal("refresh 格式")
	}
	if h.S.refreshSecret(sid, "seed-a", 0) == h.S.refreshSecret(sid, "seed-b", 0) {
		t.Fatal("种子不同，算出的值应不同")
	}
	for _, seed := range []string{"", "guessed-seed"} {
		forged := formatRefresh(sid, 0, h.S.refreshSecret(sid, seed, 0))
		if _, err := h.S.Refresh(ctx, authn.AudiencePublic, forged); !errors.Is(err, errSessionInvalid) {
			t.Fatalf("种子 %q 伪造的值应 401: %v", seed, err)
		}
		for _, gen := range []uint64{5, 100} {
			if _, err := h.S.Refresh(ctx, authn.AudiencePublic, formatRefresh(sid, gen, h.S.refreshSecret(sid, seed, gen))); !errors.Is(err, errSessionInvalid) {
				t.Fatalf("种子 %q 第 %d 代伪造的值应 401: %v", seed, gen, err)
			}
		}
	}
	if n := countAudit(h, "session.refresh_reuse"); n != 0 {
		t.Fatalf("伪造值不应写重放审计: %d", n)
	}
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh); err != nil {
		t.Fatalf("真 refresh 应仍可用: %v", err)
	}
}

// A 会话的真值配上 B 的 sid：不是 B 签发的，只回 401，两条会话都不受影响。
func TestRefreshOtherSessionValueDoesNotRevoke(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("a@example.com", "pass", UserStatusActive)
	h.Users.addUser("b@example.com", "pass", UserStatusActive)
	a, err := h.S.Login(ctx, authn.AudiencePublic, "a@example.com", "pass")
	if err != nil {
		t.Fatalf("login a: %v", err)
	}
	b, err := h.S.Login(ctx, authn.AudiencePublic, "b@example.com", "pass")
	if err != nil {
		t.Fatalf("login b: %v", err)
	}
	_, genA, secretA, _ := parseRefresh(a.Refresh)
	sidB, _, _, _ := parseRefresh(b.Refresh)
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, formatRefresh(sidB, genA, secretA)); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("套用别的会话的值应 401: %v", err)
	}
	if n := countAudit(h, "session.refresh_reuse"); n != 0 {
		t.Fatalf("不应写重放审计: %d", n)
	}
	for _, raw := range []string{a.Refresh, b.Refresh} {
		if _, err := h.S.Refresh(ctx, authn.AudiencePublic, raw); err != nil {
			t.Fatalf("两条会话都应照常: %v", err)
		}
	}
}

// 会话被盗后用户拿着被换下的旧代点退出：也吊销整条会话，攻击者手里的当前那枚一起失效。
func TestLogoutWithOlderGenerationRevokes(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("oldout@example.com", "pass", UserStatusActive)
	tok, err := h.S.Login(ctx, authn.AudiencePublic, "oldout@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	first, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh)
	if err != nil {
		t.Fatalf("攻击者第一次轮换: %v", err)
	}
	second, err := h.S.Refresh(ctx, authn.AudiencePublic, first.Refresh)
	if err != nil {
		t.Fatalf("攻击者第二次轮换: %v", err)
	}
	if err := h.S.Logout(ctx, authn.AudiencePublic, tok.Refresh); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, second.Refresh); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("旧代退出后攻击者那枚应失效: %v", err)
	}
	if _, err := h.S.Authenticate(ctx, authn.AudiencePublic, second.AccessToken); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("旧代退出后攻击者的 access 应失效: %v", err)
	}
}

// 没有种子的会话（不该出现，防御）：refresh 与 logout 都不认它的任何值，只回 401 / 当作找不到，会话还在、不写审计。
func TestRefreshEmptySeedNeverAuthentic(t *testing.T) {
	h := newTestService(t)
	ctx := testCtx("203.0.113.7")
	h.Users.addUser("noseed@example.com", "pass", UserStatusActive)
	tok, err := h.S.Login(ctx, authn.AudiencePublic, "noseed@example.com", "pass")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	sid, _, _, _ := parseRefresh(tok.Refresh)
	if err := h.RDB.HDel(ctx, sessKey(sid), "rt_seed").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.S.Refresh(ctx, authn.AudiencePublic, tok.Refresh); !errors.Is(err, errSessionInvalid) {
		t.Fatalf("没有种子时 refresh 应 401: %v", err)
	}
	if err := h.S.Logout(ctx, authn.AudiencePublic, tok.Refresh); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := h.S.Authenticate(ctx, authn.AudiencePublic, tok.AccessToken); err != nil {
		t.Fatalf("会话应还在: %v", err)
	}
	if n := countAudit(h, "session.refresh_reuse") + countAudit(h, "user.logout"); n != 0 {
		t.Fatalf("不应写审计: %d", n)
	}
}
