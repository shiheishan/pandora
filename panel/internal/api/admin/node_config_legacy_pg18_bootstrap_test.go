// [INPUT]: 依赖 domain/nodefabric 的 IssueBootstrapToken / Bootstrap / LookupIdentity / FetchConfig，依赖 node_config_legacy_pg18_test.go 的夹具与共用断言
// [OUTPUT]: 包内提供 nodeConfigPG18BootstrapPublicKey（按标签派生确定性节点公钥，_materialize 与 _lock 共用）与 runNodeConfigPG18BootstrapSecurityBatch
// [POS]: TestNodeConfigLegacyPG18 的引导安全批次：令牌与节点名绑定、旧令牌拒绝、名字规范化、停用池发证与用证拒绝、既有池绑定、终态节点与终态身份拒绝
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func nodeConfigPG18BootstrapPublicKey(label string) string {
	seed := sha256.Sum256([]byte("node-config-pg18-bootstrap-key/" + label))
	pub := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub)
}

func runNodeConfigPG18BootstrapSecurityBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appPool *platformdb.Pool, signer *platformcrypto.Signer) {
	t.Helper()
	service := nodefabric.NewService(appPool, signer)
	t.Run("bootstrap token is bound to its issued node name", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		targetName := "bootstrap-target-" + fx.suffix
		victimName := "bootstrap-victim-" + fx.suffix
		bootstrapNode := func(name string, keyByte byte) *nodefabric.BootstrapOutput {
			t.Helper()
			issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
				ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
			})
			if err != nil {
				t.Fatalf("issue initial token for %s: %v", name, err)
			}
			out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
				Token: issued.Token, NodeName: name,
				PublicKey: nodeConfigPG18BootstrapPublicKey(fmt.Sprintf("%s/%d", name, keyByte)),
				AgentVer:  "pg18-binding", Hostname: name + ".example.invalid",
			})
			if err != nil || out == nil || out.Serial != 1 {
				t.Fatalf("initial bootstrap for %s out=%+v err=%v", name, out, err)
			}
			return out
		}
		target := bootstrapNode(targetName, 0x51)
		victim := bootstrapNode(victimName, 0x52)
		issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: targetName, PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue target rotation token: %v", err)
		}
		publicKey := nodeConfigPG18BootstrapPublicKey(targetName + "/rotation")
		beforeWrongName := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		wrongOut, wrongErr := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: victimName, PublicKey: publicKey,
			AgentVer: "pg18-binding", Hostname: "wrong.example.invalid",
		})
		if wrongOut != nil {
			t.Fatalf("wrong-name bootstrap returned output %+v", wrongOut)
		}
		var wrongHTTP *httpx.Error
		if !errors.As(wrongErr, &wrongHTTP) || wrongHTTP.Code != httpx.CodeUnauthorized {
			t.Fatalf("wrong-name bootstrap error=%v, want unauthorized", wrongErr)
		}
		if afterWrongName := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); afterWrongName != beforeWrongName {
			t.Fatal("wrong-name bootstrap mutated token, node, identity, server or audit state")
		}

		correctOut, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: targetName, PublicKey: publicKey,
			AgentVer: "pg18-binding", Hostname: "bound.example.invalid",
		})
		if err != nil || correctOut == nil || correctOut.NodeID != target.NodeID || correctOut.Serial != 2 {
			t.Fatalf("correct-name bootstrap out=%+v err=%v", correctOut, err)
		}
		var used, consumed, targetActive, targetRevoked, victimActive, nodes, audits int
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1 AND used_count=1),
			(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1 AND consumed_at IS NOT NULL),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='active' AND serial=2),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='revoked' AND serial=1),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$3 AND status='active' AND serial=1),
			(SELECT count(*) FROM nodes WHERE tenant_id=$1 AND id IN ($2,$3)),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.bootstrap')`,
			fx.tenant, target.NodeID, victim.NodeID).Scan(&used, &consumed, &targetActive, &targetRevoked,
			&victimActive, &nodes, &audits); err != nil {
			t.Fatalf("verify name-bound bootstrap: %v", err)
		}
		if used != 3 || consumed != 3 || targetActive != 1 || targetRevoked != 1 ||
			victimActive != 1 || nodes != 2 || audits != 3 {
			t.Fatalf("name-bound bootstrap used/consumed/target_active/target_revoked/victim_active/nodes/audits=%d/%d/%d/%d/%d/%d/%d",
				used, consumed, targetActive, targetRevoked, victimActive, nodes, audits)
		}
		t.Log("marker=node_config_pg18_bootstrap_token_name_binding_ok")
	})

	t.Run("legacy unbound bootstrap token fails closed", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		rawToken := "legacy-unbound-" + fx.suffix
		if _, err := admin.Exec(ctx, `INSERT INTO bootstrap_tokens
			(tenant_id,token_hash,pool_id,expires_at,created_by)
			VALUES($1,$2,$3,now()+interval '20 minutes',$4)`,
			fx.tenant, platformcrypto.HashToken(rawToken), fx.pool, fx.actor); err != nil {
			t.Fatalf("seed legacy unbound bootstrap token: %v", err)
		}
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: rawToken, NodeName: "legacy-target-" + fx.suffix,
			PublicKey: nodeConfigPG18BootstrapPublicKey(fx.suffix + "/legacy-unbound"),
			AgentVer:  "pg18-binding",
		})
		if out != nil {
			t.Fatalf("legacy unbound token returned output %+v", out)
		}
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeUnauthorized {
			t.Fatalf("legacy unbound token error=%v, want unauthorized", err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("legacy unbound token changed tenant business state")
		}
		t.Log("marker=node_config_pg18_bootstrap_legacy_token_refusal_ok")
	})

	t.Run("bootstrap node name canonicalization is stable", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		canonical := "canonical-" + fx.suffix
		issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: "  " + canonical + "  ", PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue canonical bootstrap token: %v", err)
		}
		out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: "\t" + canonical + "\n",
			PublicKey: nodeConfigPG18BootstrapPublicKey(fx.suffix + "/canonical"),
			AgentVer:  "pg18-binding",
		})
		if err != nil || out == nil {
			t.Fatalf("canonical bootstrap out=%+v err=%v", out, err)
		}
		var storedName string
		if err := admin.QueryRow(ctx, `SELECT name FROM nodes WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, out.NodeID).Scan(&storedName); err != nil {
			t.Fatalf("read canonical bootstrap node: %v", err)
		}
		if storedName != canonical {
			t.Fatalf("canonical bootstrap stored name=%q want=%q", storedName, canonical)
		}
		t.Log("marker=node_config_pg18_bootstrap_name_canonical_ok")
	})

	t.Run("disabled pool refuses bootstrap token issue", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='disabled' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("disable bootstrap issue pool: %v", err)
		}
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		out, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: "disabled-issue-" + fx.suffix, PoolID: fx.pool, TTLMinutes: 20,
		})
		var he *httpx.Error
		if out != nil || !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("disabled pool token issue out=%+v err=%v", out, err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("disabled pool token issue changed tenant business state")
		}
		t.Log("marker=node_config_pg18_bootstrap_disabled_pool_issue_refusal_ok")
	})

	t.Run("disabled pool refuses token use without consuming it", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		name := "disabled-use-" + fx.suffix
		issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue token before disabling pool: %v", err)
		}
		var auditPool, auditNode string
		if err := admin.QueryRow(ctx, `SELECT after_digest->>'pool_id',after_digest->>'node_id'
			FROM audit_events WHERE tenant_id=$1 AND action='node.bootstrap_token.issue'
			ORDER BY occurred_at DESC,id DESC LIMIT 1`, fx.tenant).Scan(&auditPool, &auditNode); err != nil {
			t.Fatalf("read bootstrap token issue attribution: %v", err)
		}
		if auditPool != fx.pool || auditNode != "" {
			t.Fatalf("bootstrap token issue attribution pool/node=%q/%q", auditPool, auditNode)
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='disabled' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("disable bootstrap use pool: %v", err)
		}
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		bootstrap := nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: name,
			PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/disabled"),
			AgentVer:  "pg18-disabled-pool",
		}
		refused, err := service.Bootstrap(ctx, fx.tenant, bootstrap)
		var he *httpx.Error
		if refused != nil || !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("disabled pool bootstrap out=%+v err=%v", refused, err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("disabled pool bootstrap consumed token or changed business state")
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='active' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("reactivate bootstrap pool: %v", err)
		}
		accepted, err := service.Bootstrap(ctx, fx.tenant, bootstrap)
		if err != nil || accepted == nil {
			t.Fatalf("reactivated pool bootstrap out=%+v err=%v", accepted, err)
		}
		var storedPool string
		var used int
		var consumed bool
		if err := admin.QueryRow(ctx, `SELECT n.pool_id::text,t.used_count,t.consumed_at IS NOT NULL
			FROM nodes n JOIN bootstrap_tokens t ON t.tenant_id=n.tenant_id AND t.node_id=n.id
			WHERE n.tenant_id=$1 AND n.id=$2`, fx.tenant, accepted.NodeID).
			Scan(&storedPool, &used, &consumed); err != nil {
			t.Fatalf("read reactivated bootstrap state: %v", err)
		}
		if storedPool != fx.pool || used != 1 || !consumed {
			t.Fatalf("reactivated bootstrap pool/used/consumed=%s/%d/%t", storedPool, used, consumed)
		}
		t.Log("marker=node_config_pg18_bootstrap_disabled_pool_use_refusal_ok")
	})

	t.Run("existing node bootstrap is bound to its actual pool", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		name := "existing-pool-" + fx.suffix
		firstToken, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue initial existing-pool token: %v", err)
		}
		first, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: firstToken.Token, NodeName: name,
			PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/serial-1"), AgentVer: "pg18-existing-pool",
		})
		if err != nil || first == nil || first.Serial != 1 {
			t.Fatalf("initial existing-pool bootstrap out=%+v err=%v", first, err)
		}

		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='disabled' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("disable existing-node actual pool: %v", err)
		}
		beforeDisabledIssue := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		refusedIssue, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, TTLMinutes: 20,
		})
		var he *httpx.Error
		if refusedIssue != nil || !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("disabled actual pool issue out=%+v err=%v", refusedIssue, err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != beforeDisabledIssue {
			t.Fatal("disabled existing-node token issue changed business state")
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='active' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("reactivate existing-node actual pool: %v", err)
		}

		otherPool := uuid.NewString()
		if _, err := admin.Exec(ctx, `INSERT INTO node_pools(id,tenant_id,code,name,status)
			VALUES($1,$2,$3,$4,'active')`, otherPool, fx.tenant, "other-"+fx.suffix, "Other "+fx.suffix); err != nil {
			t.Fatalf("seed mismatched active pool: %v", err)
		}
		beforeMismatch := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		mismatched, mismatchErr := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, PoolID: otherPool, TTLMinutes: 20,
		})
		if mismatched != nil || !nodeConfigPG18IsConflict(mismatchErr) {
			t.Fatalf("mismatched existing-node pool issue out=%+v err=%v", mismatched, mismatchErr)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != beforeMismatch {
			t.Fatal("mismatched existing-node pool issue changed business state")
		}

		rotation, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue derived-pool rotation token: %v", err)
		}
		var auditPool, auditNode string
		if err := admin.QueryRow(ctx, `SELECT after_digest->>'pool_id',after_digest->>'node_id'
			FROM audit_events WHERE tenant_id=$1 AND action='node.bootstrap_token.issue'
			ORDER BY occurred_at DESC,id DESC LIMIT 1`, fx.tenant).Scan(&auditPool, &auditNode); err != nil {
			t.Fatalf("read existing-node token attribution: %v", err)
		}
		if auditPool != fx.pool || auditNode != first.NodeID {
			t.Fatalf("existing-node token attribution pool/node=%q/%q", auditPool, auditNode)
		}
		rotationHash := platformcrypto.HashToken("node-bootstrap-v2\x00" + name + "\x00" + rotation.Token)
		if _, err := admin.Exec(ctx, `UPDATE bootstrap_tokens SET pool_id=$3
			WHERE tenant_id=$1 AND token_hash=$2`, fx.tenant, rotationHash, otherPool); err != nil {
			t.Fatalf("tamper rotation token pool for mismatch test: %v", err)
		}
		rotationInput := nodefabric.BootstrapInput{
			Token: rotation.Token, NodeName: name,
			PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/serial-2"), AgentVer: "pg18-existing-pool",
		}
		beforeTokenMismatch := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		mismatchUse, mismatchUseErr := service.Bootstrap(ctx, fx.tenant, rotationInput)
		if mismatchUse != nil || !nodeConfigPG18IsConflict(mismatchUseErr) {
			t.Fatalf("mismatched token/actual pool bootstrap out=%+v err=%v", mismatchUse, mismatchUseErr)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != beforeTokenMismatch {
			t.Fatal("mismatched token/actual pool bootstrap changed business state")
		}
		if _, err := admin.Exec(ctx, `UPDATE bootstrap_tokens SET pool_id=$3
			WHERE tenant_id=$1 AND token_hash=$2`, fx.tenant, rotationHash, fx.pool); err != nil {
			t.Fatalf("restore rotation token actual pool: %v", err)
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='disabled' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("disable pool before identity rotation: %v", err)
		}
		beforeRotation := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		refusedRotation, err := service.Bootstrap(ctx, fx.tenant, rotationInput)
		if refusedRotation != nil || !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("disabled pool identity rotation out=%+v err=%v", refusedRotation, err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != beforeRotation {
			t.Fatal("disabled pool identity rotation consumed token or changed identity")
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='active' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("reactivate pool before identity rotation: %v", err)
		}
		second, err := service.Bootstrap(ctx, fx.tenant, rotationInput)
		if err != nil || second == nil || second.NodeID != first.NodeID || second.Serial != 2 {
			t.Fatalf("reactivated existing-node rotation out=%+v err=%v", second, err)
		}
		var active, revoked, used int
		var consumed bool
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='active'),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='revoked'),
			used_count,consumed_at IS NOT NULL FROM bootstrap_tokens
			WHERE tenant_id=$1 AND token_hash=$3`, fx.tenant, first.NodeID,
			rotationHash).
			Scan(&active, &revoked, &used, &consumed); err != nil {
			t.Fatalf("read existing-node rotation state: %v", err)
		}
		if active != 1 || revoked != 1 || used != 1 || !consumed {
			t.Fatalf("existing-node rotation active/revoked/used/consumed=%d/%d/%d/%t",
				active, revoked, used, consumed)
		}
		t.Log("marker=node_config_pg18_bootstrap_existing_pool_binding_ok")
	})

	t.Run("terminal nodes refuse a valid bound bootstrap token", func(t *testing.T) {
		for _, tc := range []struct {
			status  string
			serving string
		}{
			{status: "retired", serving: "retired"},
			{status: "destroyed", serving: "retired"},
			{status: "standby", serving: "retired"},
		} {
			fx := seedNodeConfigPG18Fixture(t, ctx, admin)
			name := fmt.Sprintf("terminal-%s-%s", tc.status, fx.suffix)
			if _, err := admin.Exec(ctx, `INSERT INTO nodes
				(tenant_id,name,pool_id,status,serving_status) VALUES($1,$2,$3,$4,$5)`,
				fx.tenant, name, fx.pool, tc.status, tc.serving); err != nil {
				t.Fatalf("seed %s/%s terminal node: %v", tc.status, tc.serving, err)
			}
			refusedIssue, issueErr := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
				ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
			})
			if refusedIssue != nil || !nodeConfigPG18IsConflict(issueErr) {
				t.Fatalf("terminal %s/%s token issue out=%+v err=%v", tc.status, tc.serving, refusedIssue, issueErr)
			}
			// Issue refuses terminal targets before a usable token exists. To exercise
			// Bootstrap's independent fail-closed guard, seed a correctly v2-hashed,
			// node-ID-bound row exactly as a previously issued token would look.
			rawToken := "terminal-bound-" + tc.status + "-" + fx.suffix
			var nodeID string
			if err := admin.QueryRow(ctx, `SELECT id::text FROM nodes WHERE tenant_id=$1 AND name=$2`,
				fx.tenant, name).Scan(&nodeID); err != nil {
				t.Fatalf("read %s terminal node id: %v", tc.status, err)
			}
			if _, err := admin.Exec(ctx, `INSERT INTO bootstrap_tokens
				(tenant_id,token_hash,pool_id,node_id,expires_at,created_by)
				VALUES($1,$2,$3,$4,now()+interval '20 minutes',$5)`,
				fx.tenant, platformcrypto.HashToken("node-bootstrap-v2\x00"+name+"\x00"+rawToken),
				fx.pool, nodeID, fx.actor); err != nil {
				t.Fatalf("seed %s terminal bound token: %v", tc.status, err)
			}
			before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
			out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
				Token: rawToken, NodeName: name,
				PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/terminal"),
				AgentVer:  "pg18-binding",
			})
			if out != nil || !nodeConfigPG18IsConflict(err) {
				t.Fatalf("terminal %s/%s bootstrap out=%+v err=%v", tc.status, tc.serving, out, err)
			}
			if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
				t.Fatalf("terminal %s/%s bootstrap changed business state", tc.status, tc.serving)
			}
		}
		t.Log("marker=node_config_pg18_bootstrap_terminal_refusal_ok")
	})

	t.Run("terminal lifecycle closes identity and config lookup", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		name := "terminal-identity-" + fx.suffix
		issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue terminal identity token: %v", err)
		}
		out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: name,
			PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/active"), AgentVer: "pg18-binding",
		})
		if err != nil || out == nil {
			t.Fatalf("bootstrap terminal identity node out=%+v err=%v", out, err)
		}
		h := &handlers{d: Deps{Pool: appPool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
		setStatus := func(next string) {
			t.Helper()
			var rowVersion int64
			if err := admin.QueryRow(ctx, `SELECT row_version FROM nodes WHERE tenant_id=$1 AND id=$2`,
				fx.tenant, out.NodeID).Scan(&rowVersion); err != nil {
				t.Fatalf("read %s transition row version: %v", next, err)
			}
			requestBody, err := json.Marshal(nodeStatusReq{
				RowVersion: rowVersion, Status: next, Reason: "pg18 lifecycle " + next,
			})
			if err != nil {
				t.Fatalf("marshal %s transition request: %v", next, err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/nodes/"+out.NodeID+"/status", bytes.NewReader(requestBody))
			req.Header.Set("Content-Type", "application/json")
			route := chi.NewRouteContext()
			route.URLParams.Add("id", out.NodeID)
			reqCtx := context.WithValue(ctx, chi.RouteCtxKey, route)
			reqCtx = httpx.WithTenantID(reqCtx, fx.tenant)
			reqCtx = httpx.WithRequestID(reqCtx, "nodecfg-pg18-lifecycle-"+next)
			reqCtx = httpx.WithPrincipal(reqCtx, &httpx.Principal{
				Kind: "admin", Audience: "admin", UserID: fx.actor, TenantID: fx.tenant,
			})
			recorder := httptest.NewRecorder()
			h.nodeSetStatus(recorder, req.WithContext(reqCtx))
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s lifecycle status=%d body=%s", next, recorder.Code, recorder.Body.String())
			}
		}
		for _, next := range []string{"attesting", "installing", "validating", "standby", "retired"} {
			setStatus(next)
		}
		identity, identityErr := service.LookupIdentity(ctx, fx.tenant, out.NodeID)
		if identity != nil {
			t.Fatalf("terminal LookupIdentity returned %+v", identity)
		}
		var identityHTTP *httpx.Error
		if !errors.As(identityErr, &identityHTTP) || identityHTTP.Code != httpx.CodeUnauthorized {
			t.Fatalf("terminal LookupIdentity error=%v, want unauthorized", identityErr)
		}
		_, fetchErr := service.FetchConfig(ctx, fx.tenant, out.NodeID)
		assertNodeConfigPG18NotFoundNeutral(t, "terminal identity FetchConfig", fetchErr)
		var active, revoked int
		var status, serving string
		var desiredIsNull bool
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='active'),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='revoked'),
			n.status,n.serving_status,n.desired_config_version IS NULL
			FROM nodes n WHERE n.tenant_id=$1 AND n.id=$2`, fx.tenant, out.NodeID).
			Scan(&active, &revoked, &status, &serving, &desiredIsNull); err != nil {
			t.Fatalf("read terminal active identity fixture: %v", err)
		}
		if active != 0 || revoked != 1 || status != "retired" || serving != "retired" || !desiredIsNull {
			t.Fatalf("terminal identity state active/revoked/status/serving/desired_null=%d/%d/%s/%s/%t",
				active, revoked, status, serving, desiredIsNull)
		}
		t.Log("marker=node_config_pg18_terminal_identity_refusal_ok")
	})
}
