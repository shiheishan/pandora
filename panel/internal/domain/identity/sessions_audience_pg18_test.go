// [INPUT]: 依赖 logout_pg18_test.go 的 openLogoutPG18Fixture（一次性 PG18 库护栏），依赖 sessions.go 的 ListActiveSessions / RevokeSession
// [OUTPUT]: 对外提供 TestSelfServiceSessionsPG18AudienceIsolation
// [POS]: domain/identity 的 PG18 反向测试：门户自助会话管理看不到、也踢不掉同一用户的后台会话（缺陷 6）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package identity

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestSelfServiceSessionsPG18AudienceIsolation(t *testing.T) {
	ctx, admin, app := openLogoutPG18Fixture(t)

	// 与 logout 用例共用一个库，租户与会话 ID 用不同的前缀隔开
	const (
		tenantID      = "72000000-0000-7000-8000-000000000001"
		userID        = "72000000-0000-7000-8000-000000000011"
		currentPublic = "72000000-0000-7000-8000-000000000021"
		otherPublic   = "72000000-0000-7000-8000-000000000022"
		adminSession  = "72000000-0000-7000-8000-000000000023"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'sessions-audience-pg18', 'Sessions Audience PG18', 'USD')`, []any{tenantID}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ($2, $1, 'sessions-audience@example.test', 'Audience', 'active')`, []any{tenantID, userID}},
		{`INSERT INTO sessions (id, tenant_id, user_id, audience, auth_methods, expires_at) VALUES
		    ($3, $1, $2, 'public', ARRAY['password'], now() + interval '30 days'),
		    ($4, $1, $2, 'public', ARRAY['password'], now() + interval '30 days'),
		    ($5, $1, $2, 'admin',  ARRAY['password'], now() + interval '30 days')`,
			[]any{tenantID, userID, currentPublic, otherPublic, adminSession}},
		{`INSERT INTO refresh_tokens (tenant_id, session_id, user_id, token_hash, status, expires_at)
		  VALUES ($1, $3, $2, decode('a1', 'hex'), 'active', now() + interval '30 days')`,
			[]any{tenantID, userID, adminSession}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed sessions audience fixture: %v", err)
		}
	}

	svc := &Service{pool: app}

	listed, err := svc.ListActiveSessions(ctx, tenantID, userID, currentPublic)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range listed {
		seen[s.ID] = true
	}
	if len(listed) != 2 || !seen[currentPublic] || !seen[otherPublic] || seen[adminSession] {
		t.Fatalf("portal session list must hold exactly the two public sessions, got %+v", listed)
	}

	// 反向：拿门户令牌去踢后台会话，必须像「不存在」一样被拒，且什么都不改
	err = svc.RevokeSession(ctx, tenantID, userID, adminSession, currentPublic)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeNotFound {
		t.Fatalf("revoking an admin session from the portal must be not_found, got %v", err)
	}
	var adminRevoked bool
	var adminRefreshActive int
	if err := admin.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM sessions WHERE id=$1`, adminSession).Scan(&adminRevoked); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE session_id=$1 AND status='active'`, adminSession).Scan(&adminRefreshActive); err != nil {
		t.Fatal(err)
	}
	if adminRevoked || adminRefreshActive != 1 {
		t.Fatalf("admin session touched by portal revoke: revoked=%v active_refresh=%d", adminRevoked, adminRefreshActive)
	}

	// 正向：自己的另一个门户会话照常能踢
	if err := svc.RevokeSession(ctx, tenantID, userID, otherPublic, currentPublic); err != nil {
		t.Fatalf("revoke own public session: %v", err)
	}
	var otherRevoked bool
	if err := admin.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM sessions WHERE id=$1`, otherPublic).Scan(&otherRevoked); err != nil {
		t.Fatal(err)
	}
	if !otherRevoked {
		t.Fatal("own public session was not revoked")
	}
}
