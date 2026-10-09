package nodefabric

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/bindingcontract"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// serverBindingPG18 是 server_binding 域的一次性库，取值与 run-pg18-gates.sh 的 DOMAINS 一一对应。
var serverBindingPG18 = pg18test.Fixture{
	Domain:         "SERVER_BINDING",
	DatabasePrefix: "pandora_server_binding",
	MarkerTable:    "pandora_server_binding_test_marker",
	CommentTag:     "pandora-server-binding-pg18",
}

// bindingMachine 是一台模拟机器：服务器身份签名钥与 X25519 加密公钥（加密钥只登记不使用）。
type bindingMachine struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	enc  []byte
}

func newBindingMachine(t *testing.T) bindingMachine {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc := make([]byte, 32)
	if _, err := rand.Read(enc); err != nil {
		t.Fatal(err)
	}
	return bindingMachine{pub: pub, priv: priv, enc: enc}
}

func (m bindingMachine) begin(requestID, token string) ServerEnrollmentBeginInput {
	sum := sha256.Sum256([]byte(requestID + "|" + base64.StdEncoding.EncodeToString(m.pub)))
	return ServerEnrollmentBeginInput{
		Token: token, RequestID: requestID, PublicKey: base64.StdEncoding.EncodeToString(m.pub),
		EncKEM: bindingcontract.EncKEM, EncPublicKey: base64.StdEncoding.EncodeToString(m.enc),
		AgentVersion: "v1.6.0", Features: []string{"server-binding-v1"},
		Capabilities: []byte(`{"protocols":["vless","hysteria2"]}`), Hostname: "pg18-host",
		CPUCores: 2, MemoryMB: 2048, DiskGB: 40, BeginRequestSHA256: sum[:],
	}
}

func serverCommitInput(enrollmentID, salt string) ServerEnrollmentCommitInput {
	sum := sha256.Sum256([]byte("commit|" + enrollmentID + "|" + salt))
	return ServerEnrollmentCommitInput{EnrollmentID: enrollmentID, CommitRequestSHA256: sum[:],
		AgentVersion: "v1.6.0", Architecture: "amd64", BinarySHA256: strings.Repeat("1", 64),
		ConfigSHA256: strings.Repeat("2", 64), UnitSHA256: strings.Repeat("3", 64), PreflightSHA256: strings.Repeat("4", 64)}
}

// signedPreimage 按合约 §2.2 给一个服务器请求签名，返回原像与签名。
func (m bindingMachine) signedPreimage(t *testing.T, tenantID, serverID string, serial int64, target string) ([]byte, []byte) {
	t.Helper()
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	pre, err := bindingcontract.ServerRequestPreimage(bindingcontract.ServerRequestFields{
		Method: "GET", Target: target, TenantID: tenantID, ServerID: serverID, Serial: serial,
		Timestamp: time.Now().UTC().Format(time.RFC3339), Nonce: base64.RawURLEncoding.EncodeToString(nonce),
		BodySHA256: bindingcontract.BodySHA256(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	return pre, ed25519.Sign(m.priv, pre)
}

func wantHTTPCode(t *testing.T, err error, code httpx.Code) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// TestServerBindingPG18 覆盖服务器级绑定 P1 的完成标准：接入幂等、同一面板第二台机器被拒、
// 令牌只能用一次、吊销后验签失败（网关 401）、RLS 跨租户 0 行，以及删除 / 退役收回绑定。
func TestServerBindingPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, serverBindingPG18)
	seed := sha256.Sum256([]byte("server-binding-pg18-signer"))
	signer, err := platformcrypto.NewSigner(seed[:])
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(app, signer)
	svc.SetReleaseBinding(ReleaseBinding{}) // 非生产：不比对发布产物

	tenant, otherTenant := uuid.NewString(), uuid.NewString()
	actor := uuid.NewString()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name) VALUES ($1,$2,'Server Binding A'), ($3,$4,'Server Binding B')`,
		tenant, "server-binding-a-"+tenant, otherTenant, "server-binding-b-"+otherTenant)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, 'server-binding-admin@example.test', 'Server Binding Admin', 'active')`, actor, tenant)
	newServer := func(name string) string {
		t.Helper()
		var id string
		if err := admin.QueryRow(ctx, `INSERT INTO servers (tenant_id, name) VALUES ($1,$2) RETURNING id::text`,
			tenant, name+"-"+uuid.NewString()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	issue := func(serverID string) *ServerBindingTokenOutput {
		t.Helper()
		out, err := svc.IssueServerBindingToken(ctx, tenant, IssueServerBindingTokenInput{
			ActorID: actor, ServerID: serverID, PanelURL: "http://127.0.0.1:8080"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	status := func(serverID string) *ServerBindingStatus {
		t.Helper()
		out, err := svc.ServerBinding(ctx, tenant, serverID)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	serverID := newServer("bind")
	machineA, machineB := newBindingMachine(t), newBindingMachine(t)
	var enrollA *ServerEnrollmentOutput

	t.Run("enrollment is idempotent", func(t *testing.T) {
		if st := status(serverID); st.State != ServerBindingUnbound || st.Identity != nil {
			t.Fatalf("fresh server should be unbound: %+v", st)
		}
		tok := issue(serverID)
		if !strings.Contains(tok.Command, tok.PanelKey) || strings.Contains(tok.Command, tok.Token) {
			t.Fatal("command must carry --panel-key and never the token itself")
		}
		requestID := uuid.NewString()
		first, err := svc.BeginServerEnrollment(ctx, tenant, machineA.begin(requestID, tok.Token))
		if err != nil {
			t.Fatal(err)
		}
		retry, err := svc.BeginServerEnrollment(ctx, tenant, machineA.begin(requestID, tok.Token))
		if err != nil {
			t.Fatalf("begin retry: %v", err)
		}
		if !reflect.DeepEqual(first, retry) || first.ServerID != serverID || first.TenantID != tenant || first.Serial != 1 {
			t.Fatalf("begin retry changed the response: %+v vs %+v", first, retry)
		}
		// 合约 §3.1：pdnd 用 --panel-key 核对响应里的配置公钥，并核对 key id
		pub, _ := base64.StdEncoding.DecodeString(first.ConfigPublicKey)
		if bindingcontract.CheckPanelKey(tok.PanelKey, pub) != nil || bindingcontract.KeyID(pub) != first.ConfigKeyID {
			t.Fatal("begin response does not match the pinned panel key")
		}
		if st := status(serverID); st.State != ServerBindingBinding || st.PendingEnrollment == nil {
			t.Fatalf("pending enrollment should show as binding: %+v", st)
		}
		// 同一 request_id 换一份证据（另一把公钥）回 409
		_, err = svc.BeginServerEnrollment(ctx, tenant, machineB.begin(requestID, tok.Token))
		wantHTTPCode(t, err, httpx.CodeConflict)

		commit := serverCommitInput(first.EnrollmentID, "a")
		done, err := svc.CommitServerEnrollment(ctx, tenant, commit)
		if err != nil || done.State != "committed" {
			t.Fatalf("commit: %+v %v", done, err)
		}
		again, err := svc.CommitServerEnrollment(ctx, tenant, commit)
		if err != nil || !reflect.DeepEqual(again, done) {
			t.Fatalf("idempotent commit: %+v %v", again, err)
		}
		_, err = svc.CommitServerEnrollment(ctx, tenant, serverCommitInput(first.EnrollmentID, "other"))
		wantHTTPCode(t, err, httpx.CodeConflict)
		var identities int
		var host string
		if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM server_identities WHERE tenant_id=$1 AND server_id=$2),
			coalesce((SELECT hostname FROM servers WHERE id=$2),'')`, tenant, serverID).Scan(&identities, &host); err != nil {
			t.Fatal(err)
		}
		if identities != 1 || host != "pg18-host" {
			t.Fatalf("identities=%d host=%q", identities, host)
		}
		st := status(serverID)
		if st.State != ServerBindingBound || st.Identity == nil || st.Identity.Serial != 1 ||
			st.Identity.KeyID != bindingcontract.KeyID(machineA.pub) || st.BindingTokensPending != 0 {
			t.Fatalf("bound status: %+v", st)
		}
		enrollA = first
	})

	t.Run("binding token is single use", func(t *testing.T) {
		if enrollA == nil {
			t.Skip("depends on enrollment")
		}
		// 用过的令牌（不论新 request_id）一律 401
		fresh := newServer("single")
		tok := issue(fresh)
		if _, err := svc.BeginServerEnrollment(ctx, tenant, newBindingMachine(t).begin(uuid.NewString(), tok.Token)); err != nil {
			t.Fatal(err)
		}
		_, err := svc.BeginServerEnrollment(ctx, tenant, newBindingMachine(t).begin(uuid.NewString(), tok.Token))
		wantHTTPCode(t, err, httpx.CodeUnauthorized)
		// 过期令牌 401；新签发作废同一台服务器的旧令牌
		other := newServer("expired")
		old := issue(other)
		newer := issue(other)
		_, err = svc.BeginServerEnrollment(ctx, tenant, newBindingMachine(t).begin(uuid.NewString(), old.Token))
		wantHTTPCode(t, err, httpx.CodeUnauthorized)
		must(`UPDATE bootstrap_tokens SET expires_at=now()-interval '1 second' WHERE token_hash=$1`,
			serverBindingTokenHash(newer.Token))
		_, err = svc.BeginServerEnrollment(ctx, tenant, newBindingMachine(t).begin(uuid.NewString(), newer.Token))
		wantHTTPCode(t, err, httpx.CodeUnauthorized)
		// 节点接入令牌的哈希域查不到绑定令牌
		_, err = svc.BeginServerEnrollment(ctx, tenant, newBindingMachine(t).begin(uuid.NewString(), "not-a-token"))
		wantHTTPCode(t, err, httpx.CodeUnauthorized)
	})

	t.Run("second machine for the same server is refused", func(t *testing.T) {
		if enrollA == nil {
			t.Skip("depends on enrollment")
		}
		// 已绑定的服务器不再签发绑定令牌
		_, err := svc.IssueServerBindingToken(ctx, tenant, IssueServerBindingTokenInput{
			ActorID: actor, ServerID: serverID, PanelURL: "http://127.0.0.1:8080"})
		wantHTTPCode(t, err, httpx.CodeConflict)
		// 绕过签发直接塞一张令牌（模拟绑定前漏出去的那张）：begin 照常受理（pdnd 自判幂等），commit 409
		token := "slipped-" + uuid.NewString()
		must(`INSERT INTO bootstrap_tokens (tenant_id, token_hash, server_id, kind, max_uses, expires_at)
			VALUES ($1,$2,$3,'server',1,now()+interval '1 hour')`, tenant, serverBindingTokenHash(token), serverID)
		begun, err := svc.BeginServerEnrollment(ctx, tenant, machineB.begin(uuid.NewString(), token))
		if err != nil {
			t.Fatal(err)
		}
		if begun.ServerID != serverID || begun.Serial != 2 {
			t.Fatalf("begin on a bound server: %+v", begun)
		}
		_, err = svc.CommitServerEnrollment(ctx, tenant, serverCommitInput(begun.EnrollmentID, "b"))
		wantHTTPCode(t, err, httpx.CodeConflict)
		// 唯一部分索引兜底：库里也塞不进第二个 active 身份
		_, err = admin.Exec(ctx, `INSERT INTO server_identities (tenant_id, server_id, serial, public_key, fingerprint,
			enc_kem, enc_public_key, agent_version, enrollment_id)
			SELECT tenant_id, server_id, 99, public_key, fingerprint, enc_kem, enc_public_key, agent_version, id
			  FROM server_enrollments WHERE id=$1`, begun.EnrollmentID)
		if !db.IsUniqueViolation(err) || db.ConstraintName(err) != "uq_server_identities_active" {
			t.Fatalf("second active identity was not refused by the index: %v", err)
		}
		// pdnd 判定是同一面板同一服务器后 abort；重复 abort 幂等
		for i := 0; i < 2; i++ {
			out, err := svc.AbortServerEnrollment(ctx, tenant, begun.EnrollmentID, "already bound")
			if err != nil || out.State != "aborted" {
				t.Fatalf("abort #%d: %+v %v", i, out, err)
			}
		}
		_, err = svc.AbortServerEnrollment(ctx, tenant, enrollA.EnrollmentID, "late")
		wantHTTPCode(t, err, httpx.CodeConflict)
	})

	t.Run("nonce replay is refused per server", func(t *testing.T) {
		nonce := make([]byte, 16)
		fp := sha256.Sum256([]byte("fingerprint"))
		now := time.Now().UTC()
		if err := svc.ClaimServerRequestNonce(ctx, tenant, serverID, nonce, fp[:], now); err != nil {
			t.Fatal(err)
		}
		if err := svc.ClaimServerRequestNonce(ctx, tenant, serverID, nonce, fp[:], now); !errors.Is(err, ErrServerIdentityInvalid) {
			t.Fatalf("replayed nonce: %v", err)
		}
		if err := svc.ClaimServerRequestNonce(ctx, tenant, newServer("nonce"), nonce, fp[:], now); err != nil {
			t.Fatalf("same nonce on another server: %v", err)
		}
	})

	t.Run("revocation fails signed requests", func(t *testing.T) {
		if enrollA == nil {
			t.Skip("depends on enrollment")
		}
		pre, sig := machineA.signedPreimage(t, tenant, serverID, 1, "/v1/servers/manifest")
		if _, err := svc.VerifyServerRequest(ctx, tenant, serverID, 1, pre, sig); err != nil {
			t.Fatalf("active identity: %v", err)
		}
		if err := svc.VerifyServerEnrollmentRequest(ctx, tenant, enrollA.EnrollmentID, serverID, 1, pre, sig); err != nil {
			t.Fatalf("committed enrollment: %v", err)
		}
		if _, err := svc.VerifyServerRequest(ctx, tenant, serverID, 2, pre, sig); !errors.Is(err, ErrServerIdentityInvalid) {
			t.Fatalf("wrong serial: %v", err)
		}
		bad, badSig := machineB.signedPreimage(t, tenant, serverID, 1, "/v1/servers/manifest")
		if _, err := svc.VerifyServerRequest(ctx, tenant, serverID, 1, bad, badSig); !errors.Is(err, ErrServerSignatureMismatch) {
			t.Fatalf("foreign key signature: %v", err)
		}
		st, err := svc.UnbindServer(ctx, tenant, actor, serverID)
		if err != nil {
			t.Fatal(err)
		}
		if st.State != ServerBindingRevoked || st.Identity == nil || st.Identity.RevokedReason == nil ||
			*st.Identity.RevokedReason != ServerRevokedUnbound {
			t.Fatalf("unbind status: %+v", st)
		}
		if _, err := svc.VerifyServerRequest(ctx, tenant, serverID, 1, pre, sig); !errors.Is(err, ErrServerIdentityInvalid) {
			t.Fatalf("revoked identity still verifies: %v", err)
		}
		if err := svc.VerifyServerEnrollmentRequest(ctx, tenant, enrollA.EnrollmentID, serverID, 1, pre, sig); !errors.Is(err, ErrServerIdentityInvalid) {
			t.Fatalf("revoked enrollment still verifies: %v", err)
		}
		// 吊销不可逆、密钥材料不可改（触发器，迁移角色也一样）
		if _, err := admin.Exec(ctx, `UPDATE server_identities SET status='active', revoked_at=NULL, revoked_reason=NULL
			WHERE tenant_id=$1 AND server_id=$2`, tenant, serverID); err == nil {
			t.Fatal("revoked identity was reactivated")
		}
		if _, err := svc.UnbindServer(ctx, tenant, actor, serverID); err != nil {
			t.Fatalf("repeated unbind: %v", err)
		}
		// 解绑后可以换机重绑，serial 单调前进
		tok := issue(serverID)
		machineC := newBindingMachine(t)
		begun, err := svc.BeginServerEnrollment(ctx, tenant, machineC.begin(uuid.NewString(), tok.Token))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CommitServerEnrollment(ctx, tenant, serverCommitInput(begun.EnrollmentID, "c")); err != nil {
			t.Fatal(err)
		}
		if st := status(serverID); st.State != ServerBindingBound || st.Identity.Serial != 3 {
			t.Fatalf("rebind: %+v", st.Identity)
		}
		if _, err := admin.Exec(ctx, `UPDATE server_identities SET public_key=$3
			WHERE tenant_id=$1 AND server_id=$2 AND status='active'`, tenant, serverID, []byte(machineA.pub)); err == nil {
			t.Fatal("identity key material was changed")
		}
	})

	t.Run("retire and delete revoke the binding", func(t *testing.T) {
		for _, path := range []string{"retire", "delete"} {
			id := newServer(path)
			tok := issue(id)
			m := newBindingMachine(t)
			begun, err := svc.BeginServerEnrollment(ctx, tenant, m.begin(uuid.NewString(), tok.Token))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.CommitServerEnrollment(ctx, tenant, serverCommitInput(begun.EnrollmentID, path)); err != nil {
				t.Fatal(err)
			}
			var rv int64
			if err := admin.QueryRow(ctx, `SELECT row_version FROM servers WHERE id=$1`, id).Scan(&rv); err != nil {
				t.Fatal(err)
			}
			if path == "retire" {
				_, err = svc.SetServerStatus(ctx, tenant, id, SetServerStatusInput{ActorID: actor, Status: "retired", RowVersion: rv})
			} else {
				err = svc.DeleteServer(ctx, tenant, actor, id, rv)
			}
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			var reason string
			if err := admin.QueryRow(ctx, `SELECT coalesce(revoked_reason,'') FROM server_identities WHERE server_id=$1`, id).
				Scan(&reason); err != nil || reason != ServerRevokedDeleted {
				t.Fatalf("%s did not revoke the identity: %q %v", path, reason, err)
			}
			pre, sig := m.signedPreimage(t, tenant, id, 1, "/v1/servers/manifest")
			if _, err := svc.VerifyServerRequest(ctx, tenant, id, 1, pre, sig); !errors.Is(err, ErrServerIdentityInvalid) {
				t.Fatalf("%s: identity still verifies: %v", path, err)
			}
		}
	})

	t.Run("control node retires through status:batch once its server is retired", func(t *testing.T) {
		checkControlNodeBatchRetirePG18(t, ctx, admin, svc, tenant, actor)
	})

	t.Run("rls hides another tenant", func(t *testing.T) {
		checkServerBindingRLS(t, ctx, admin, app, svc, tenant, otherTenant, serverID)
	})
}

// checkServerBindingRLS：以运行角色、另一个租户的作用域去读这台服务器的绑定数据，一行也看不到；
// 服务层同样当它不存在。
func checkServerBindingRLS(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *db.Pool, svc *Service,
	tenant, otherTenant, serverID string) {
	t.Helper()
	var own int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM server_enrollments WHERE tenant_id=$1 AND server_id=$2`,
		tenant, serverID).Scan(&own); err != nil || own == 0 {
		t.Fatalf("fixture has no rows to hide: %d %v", own, err)
	}
	for _, table := range []string{"server_identities", "server_enrollments", "server_request_nonces", "bootstrap_tokens"} {
		var n int
		if err := app.QueryRowScoped(ctx, db.Scope{TenantID: otherTenant},
			`SELECT count(*) FROM `+table+` WHERE server_id=$1::uuid`, []any{serverID}, &n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s leaks %d rows across tenants", table, n)
		}
	}
	_, err := svc.ServerBinding(ctx, otherTenant, serverID)
	wantHTTPCode(t, err, httpx.CodeNotFound)
	_, err = svc.UnbindServer(ctx, otherTenant, "", serverID)
	wantHTTPCode(t, err, httpx.CodeNotFound)
	_, err = svc.IssueServerBindingToken(ctx, otherTenant, IssueServerBindingTokenInput{ServerID: serverID})
	wantHTTPCode(t, err, httpx.CodeNotFound)
}
