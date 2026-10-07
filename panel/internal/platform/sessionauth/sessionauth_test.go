package sessionauth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

func TestSessionValidityQueryFailsClosedAcrossPrincipalBoundaries(t *testing.T) {
	for _, clause := range []string{
		"revoked_at IS NOT NULL",
		"expires_at <= now()",
		"tenant_id = $1",
		"id = $2::uuid",
		"user_id = $3::uuid",
		"audience = $4",
	} {
		if !strings.Contains(validitySQL, clause) {
			t.Fatalf("session validity query is missing %q: %s", clause, validitySQL)
		}
	}
}

func TestSessionTouchIsThrottledAndScoped(t *testing.T) {
	for _, clause := range []string{
		"SET last_seen_at = now()",
		"tenant_id = $1",
		"id = $2::uuid",
		"last_seen_at < now() - interval '5 minutes'",
		// 只刷新仍有效的会话
		"EXISTS (SELECT 1 FROM sess WHERE NOT sess.revoked)",
	} {
		if !strings.Contains(touchSQL, clause) {
			t.Fatalf("session touch is missing %q: %s", clause, touchSQL)
		}
	}
	// 数据修改 CTE 必须在同一条语句里（PostgreSQL 保证它执行），而不是一条被跳过的独立语句
	if !strings.Contains(authSQL, "touch AS ("+touchSQL) ||
		!strings.Contains(authSQL, "WITH sess AS ("+validitySQL) {
		t.Fatal("validity and touch must be CTEs of the single auth statement")
	}
}

func TestAdminPermissionExpansionRequiresTenantScope(t *testing.T) {
	for _, want := range []string{"JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id",
		"rb.scope_type = 'tenant'", "rb.scope_id IS NULL", "rb.expires_at > now()", "rb.user_id = $3::uuid"} {
		if !strings.Contains(adminPermissionsSQL, want) {
			t.Fatalf("admin permission query missing %q: %s", want, adminPermissionsSQL)
		}
	}
	// 只有后台、且会话有效时才展开；门户不展开
	if !strings.Contains(authSQL, "CASE WHEN $4 = 'admin' AND NOT sess.revoked") ||
		!strings.Contains(authSQL, "THEN ARRAY("+adminPermissionsSQL+")") {
		t.Fatalf("permission expansion must be gated on admin audience: %s", authSQL)
	}
}

type fakeQuerier struct {
	revoked     bool
	permissions []string
	err         error
	calls       int
	scope       db.Scope
	sql         string
	args        []any
}

func (q *fakeQuerier) QueryRowScoped(_ context.Context, s db.Scope, sql string, args []any, dest ...any) error {
	q.calls++
	q.scope, q.sql, q.args = s, sql, args
	if q.err != nil {
		return q.err
	}
	*(dest[0].(*bool)) = q.revoked
	*(dest[1].(*[]string)) = q.permissions
	return nil
}

func TestCheckIsOneScopedRoundTrip(t *testing.T) {
	s := Session{TenantID: "t", SessionID: "s", UserID: "u", Audience: "admin"}
	q := &fakeQuerier{permissions: []string{"node.read"}}
	res, err := Check(context.Background(), q, s)
	if err != nil || res.Revoked || len(res.Permissions) != 1 {
		t.Fatalf("active admin session: res=%+v err=%v", res, err)
	}
	if q.calls != 1 || q.sql != authSQL || q.scope != (db.Scope{TenantID: "t", ActorID: "u"}) {
		t.Fatalf("calls=%d scope=%+v", q.calls, q.scope)
	}
	if len(q.args) != 4 || q.args[0] != "t" || q.args[1] != "s" || q.args[2] != "u" || q.args[3] != "admin" {
		t.Fatalf("args=%#v", q.args)
	}

	// 会话行不存在与已吊销同样按失效处理；库错误原样返回（中间件回 500，不放行）
	if res, err := Check(context.Background(), &fakeQuerier{err: pgx.ErrNoRows}, s); err != nil || !res.Revoked {
		t.Fatalf("missing row: res=%+v err=%v", res, err)
	}
	boom := errors.New("connection reset")
	if _, err := Check(context.Background(), &fakeQuerier{err: boom}, s); !errors.Is(err, boom) {
		t.Fatalf("database error = %v", err)
	}
	if res, _ := Check(context.Background(), &fakeQuerier{revoked: true, permissions: []string{"x"}}, s); !res.Revoked || res.Permissions != nil {
		t.Fatalf("revoked session carried permissions: %+v", res)
	}
}
