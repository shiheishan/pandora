// [INPUT]: 依赖 enrollment_pg18_test.go 的 openEnrollmentPG18（一次性 PG18 库护栏），依赖 uniproxy.go 的 IssueServerToken
// [OUTPUT]: 对外提供 TestIssueServerTokenPG18
// [POS]: domain/nodefabric 的 PG18 测试：server-token 签发拒绝已退出服务的节点、非法 id 回 404、成功签发写审计且不记令牌
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestIssueServerTokenPG18(t *testing.T) {
	ctx, admin, app := openEnrollmentPG18(t)

	tenant, pool, server, actor := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	live, retired, destroyed, servingRetired := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name) VALUES($1,$2,'Server Token PG18')`, []any{tenant, "server-token-" + tenant}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,$3,'Operator','active')`, []any{tenant, actor, "operator-" + actor + "@example.test"}},
		{`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'token','Token','active')`, []any{tenant, pool}},
		{`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'token-server','ready')`, []any{tenant, server}},
	}
	for _, n := range []struct{ id, name, status, serving string }{
		{live, "live-node", "active", "active"},
		{retired, "retired-node", "retired", "retired"},
		{destroyed, "destroyed-node", "destroyed", "retired"},
		{servingRetired, "serving-retired-node", "active", "retired"},
	} {
		seed = append(seed, struct {
			sql  string
			args []any
		}{`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,server_id,serving_status)
		   VALUES($2,$1,$3,$4,$5,'vless','token.invalid',443,$6,$7)`,
			[]any{tenant, n.id, n.name, pool, n.status, server, n.serving}})
	}
	for _, row := range seed {
		if _, err := admin.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed server token fixture: %v\nSQL: %s", err, row.sql)
		}
	}

	svc := NewService(app, nil)
	codeOf := func(err error) httpx.Code {
		var he *httpx.Error
		if errors.As(err, &he) {
			return he.Code
		}
		return ""
	}

	// 反向：已退出服务的节点一律 409，令牌哈希不变、不写审计
	for name, id := range map[string]string{"retired": retired, "destroyed": destroyed, "serving-retired": servingRetired} {
		if _, _, err := svc.IssueServerToken(ctx, tenant, actor, id); codeOf(err) != httpx.CodeConflict {
			t.Fatalf("%s node: err=%v, want conflict", name, err)
		}
	}
	// 反向：非法 id 与不存在的节点是 404，而不是 500
	for _, id := range []string{"not-a-uuid", uuid.NewString()} {
		if _, _, err := svc.IssueServerToken(ctx, tenant, actor, id); codeOf(err) != httpx.CodeNotFound {
			t.Fatalf("node %q: err=%v, want not_found", id, err)
		}
	}
	var refusedTokens, audits int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE tenant_id=$1 AND server_token_hash IS NOT NULL`, tenant).Scan(&refusedTokens); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.server_token.issue'`, tenant).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if refusedTokens != 0 || audits != 0 {
		t.Fatalf("refused issues left state behind tokens=%d audits=%d", refusedTokens, audits)
	}

	// 正向：在役节点签发成功，库里是令牌哈希，审计记了是谁签的，但不含令牌
	tok, nodeType, err := svc.IssueServerToken(ctx, tenant, actor, live)
	if err != nil || tok == "" || nodeType != "vless" {
		t.Fatalf("issue for live node: tok=%q type=%q err=%v", tok, nodeType, err)
	}
	var stored []byte
	if err := admin.QueryRow(ctx, `SELECT server_token_hash FROM nodes WHERE id=$1`, live).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !platformcrypto.ConstantTimeEqual(stored, platformcrypto.HashToken(tok)) {
		t.Fatal("stored server token hash does not match the issued token")
	}
	var auditActor, auditResource, digest string
	if err := admin.QueryRow(ctx, `
		SELECT actor_id::text, resource_id::text, after_digest::text FROM audit_events
		 WHERE tenant_id=$1 AND action='node.server_token.issue'`, tenant).Scan(&auditActor, &auditResource, &digest); err != nil {
		t.Fatalf("server token issue left no audit: %v", err)
	}
	if auditActor != actor || auditResource != live {
		t.Fatalf("audit actor=%s resource=%s", auditActor, auditResource)
	}
	if strings.Contains(digest, tok) || strings.Contains(digest, tok[:8]) {
		t.Fatal("audit digest leaked the server token")
	}
}
