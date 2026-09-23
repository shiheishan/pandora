package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/middleware"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// TestSignedNodeHTTPPG18 is deliberately opt-in because it requires a fresh,
// disposable PostgreSQL 18 database with all migrations already applied.
// It proves the public contracts used by pdnd without touching a deployed
// service: bootstrap, signed effective config, ordered application reports,
// heartbeat, and the runtime-bearer UniProxy data plane.
func TestSignedNodeHTTPPG18(t *testing.T) {
	if strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_FIXTURE")) != "disposable-v1" {
		t.Skip("AEGIS_EFFECTIVE_PG18_FIXTURE is not disposable-v1")
	}
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_ADMIN_DSN"))
	expectedDB := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_DATABASE"))
	if appDSN == "" || adminDSN == "" || expectedDB == "" {
		t.Skip("effective PG18 DSNs and expected database are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var dbName string
	var serverVersion int
	if err := admin.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int`).Scan(&dbName, &serverVersion); err != nil {
		t.Fatal(err)
	}
	if dbName != expectedDB || serverVersion < 180000 || serverVersion >= 190000 {
		t.Fatalf("refusing unexpected PG target: database=%q version=%d", dbName, serverVersion)
	}
	app, err := db.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	signer, err := platformcrypto.NewSigner(bytes.Repeat([]byte{0x5a}, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	svc := nodefabric.NewService(app, signer)
	server := httptest.NewServer(NewRouter(Deps{Pool: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Node: svc}))
	defer server.Close()

	const tenantID = middleware.DefaultTenantID
	nodeName := "signed-e2e-" + uuid.NewString()
	bootstrapToken := "bootstrap-" + uuid.NewString()
	tokenHash := sha256.Sum256([]byte("node-bootstrap-v2\x00" + nodeName + "\x00" + bootstrapToken))
	if _, err := admin.Exec(ctx, `INSERT INTO bootstrap_tokens(tenant_id,token_hash,expires_at)
		VALUES($1,$2,now()+interval '20 minutes')`, tenantID, tokenHash[:]); err != nil {
		t.Fatal(err)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// 两阶段接入。
	//
	// 这里原先打 /v1/nodes/bootstrap 一步拿身份，那个端点现在固定返回 426。
	// 换成 begin + commit 不只是换条路径：begin 要求节点用自己的私钥证明
	// 它持有那把公钥，commit 还要对「方法+路径+接入ID+节点ID+序号+时间戳+
	// 随机数+请求体哈希」整体签名。面板据此确认提交的就是发起的那一方，
	// 而不是中途截获了接入 ID 的任何人。
	//
	// 运行时令牌也由节点自己生成，只把哈希报上去——面板从头到尾拿不到明文。
	runtimeToken := "runtime-" + uuid.NewString()
	runtimeHash := sha256.Sum256([]byte(runtimeToken))
	requestID := uuid.NewString()
	beginBody := mustJSON(t, map[string]any{
		"token": bootstrapToken, "node_name": nodeName, "request_id": requestID,
		"public_key":           base64.StdEncoding.EncodeToString(publicKey),
		"runtime_token_sha256": base64.StdEncoding.EncodeToString(runtimeHash[:]),
		"agent_version":        "e2e-test", "hostname": "e2e-host",
	})
	beginHash := sha256.Sum256(beginBody)
	var bootstrap nodefabric.EnrollmentOutput
	doJSON(t, http.MethodPost, server.URL+"/v1/nodes/enrollments", beginBody,
		map[string]string{"X-Enrollment-Signature": base64.StdEncoding.EncodeToString(ed25519.Sign(
			privateKey, nodefabric.CanonicalEnrollmentBeginV1("/v1/nodes/enrollments", requestID, beginHash[:])))},
		http.StatusCreated, &bootstrap)
	// 配置签名密钥在 begin 阶段就下发，commit 不再重复给——节点要先能验配置
	// 签名，才谈得上把后面那些配置当真。
	if bootstrap.EnrollmentID == "" || bootstrap.NodeID == "" || bootstrap.Serial <= 0 ||
		bootstrap.ConfigKeyID != signer.KeyID() || bootstrap.ConfigPublicKey == "" {
		t.Fatalf("incomplete enrollment identity: %+v", bootstrap)
	}

	commitBody := mustJSON(t, map[string]any{
		"agent_version": "e2e-test", "architecture": "amd64",
		"binary_sha256": strings.Repeat("1", 64), "config_sha256": strings.Repeat("2", 64),
		"unit_sha256": strings.Repeat("3", 64), "preflight_sha256": strings.Repeat("4", 64),
	})
	// 用单独的变量接 commit 的结果，不要覆盖 begin 那份：commit 只回状态，
	// 不再带配置密钥，覆盖过去会把已经拿到的密钥清空。
	var committed nodefabric.EnrollmentOutput
	doEnrollmentSignedJSON(t, privateKey, bootstrap, http.MethodPost,
		server.URL+"/v1/nodes/enrollments/"+bootstrap.EnrollmentID+"/commit", commitBody, http.StatusOK, &committed)
	if committed.State != "committed" || committed.NodeID != bootstrap.NodeID {
		t.Fatalf("commit did not settle the enrollment: %+v", committed)
	}

	// Bootstrap intentionally creates a draft asset. Promote only this isolated
	// fixture to a runnable protocol so the remaining public contracts can run.
	command, err := admin.Exec(ctx, `UPDATE nodes SET serving_status='active',
		node_type='shadowsocks',server_host='127.0.0.1',server_port=14443,kernel='pandora-native',
		protocol_config='{"method":"aes-256-gcm","password":"e2e-secret"}'::jsonb,
		protocol_schema_version=1,config_validated_at=now()
		WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, bootstrap.NodeID)
	if err != nil || command.RowsAffected() != 1 {
		t.Fatalf("promote fixture node: rows=%d err=%v", command.RowsAffected(), err)
	}
	if _, err := admin.Exec(ctx, `UPDATE servers SET status='ready' WHERE tenant_id=$1 AND control_node_id=$2::uuid`, tenantID, bootstrap.NodeID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"attesting", "installing", "validating", "standby", "canary", "active"} {
		if _, err := admin.Exec(ctx, `UPDATE nodes SET status=$3 WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, bootstrap.NodeID, status); err != nil {
			t.Fatalf("advance fixture node to %s: %v", status, err)
		}
	}

	var cfg nodefabric.SignedConfig
	doSignedJSON(t, privateKey, bootstrap.NodeID, http.MethodGet, server.URL+"/v1/nodes/effective-config", nil, http.StatusOK, &cfg)
	if cfg.ConfigContract != nodefabric.EffectiveReleaseContract || cfg.ReleaseID == "" || cfg.Generation == 0 {
		t.Fatalf("invalid effective config envelope: %+v", cfg)
	}
	report := func(phase string) {
		t.Helper()
		releaseID := uuid.MustParse(cfg.ReleaseID)
		body := mustJSON(t, map[string]any{
			"report_id":  uuid.NewSHA1(releaseID, []byte(phase)).String(),
			"release_id": cfg.ReleaseID, "generation": cfg.Generation,
			"content_sha256": cfg.ContentSHA256, "phase": phase, "detail": "",
		})
		doSignedJSON(t, privateKey, bootstrap.NodeID, http.MethodPost, server.URL+"/v1/nodes/config/report", body, http.StatusOK, nil)
	}
	report("switched")
	report("health_passed")
	doSignedJSON(t, privateKey, bootstrap.NodeID, http.MethodPost, server.URL+"/v1/nodes/heartbeat",
		mustJSON(t, map[string]any{"agent_version": "e2e-test", "runtime_version": "native-e2e",
			"config_signing_key_id":        signer.KeyID(),
			"applied_effective_release_id": cfg.ReleaseID, "applied_effective_generation": cfg.Generation,
			"applied_effective_content_sha256": cfg.ContentSHA256}), http.StatusOK, nil)

	uniURL := func(path string) string {
		return fmt.Sprintf("%s/api/v1/server/UniProxy/%s?node_id=%s&node_type=shadowsocks", server.URL, path, bootstrap.NodeID)
	}
	auth := map[string]string{"Authorization": "Bearer " + runtimeToken}
	doJSON(t, http.MethodGet, uniURL("config"), nil, auth, http.StatusOK, nil)
	doJSON(t, http.MethodGet, uniURL("user"), nil, auth, http.StatusOK, nil)
	doJSON(t, http.MethodPost, uniURL("push"), []byte(`{}`), auth, http.StatusOK, nil)
	doJSON(t, http.MethodPost, uniURL("alive"), []byte(`{}`), auth, http.StatusOK, nil)

	var applications, trafficReports int
	var adoptedKeyID string
	if err := admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM node_config_applications WHERE tenant_id=$1 AND node_id=$2::uuid),
		(SELECT count(*) FROM node_traffic_reports WHERE tenant_id=$1 AND node_id=$2::uuid),
		(SELECT config_signing_key_id FROM nodes WHERE tenant_id=$1 AND id=$2::uuid)`, tenantID, bootstrap.NodeID).
		Scan(&applications, &trafficReports, &adoptedKeyID); err != nil {
		t.Fatal(err)
	}
	if applications != 2 || trafficReports != 1 {
		t.Fatalf("missing persisted evidence: applications=%d traffic_reports=%d", applications, trafficReports)
	}
	if adoptedKeyID != signer.KeyID() {
		t.Fatalf("config signing key adoption was not persisted: got=%q want=%q", adoptedKeyID, signer.KeyID())
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func doSignedJSON(t *testing.T, privateKey ed25519.PrivateKey, nodeID, method, target string, body []byte, want int, out any) {
	t.Helper()
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		t.Fatal(err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	ts := time.Now().UTC().Format(time.RFC3339)
	sum := sha256.Sum256(body)
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	payload := nodefabric.CanonicalPayloadV2(method, req.URL.Path, nodeID, ts, nonce, sum[:])
	req.Header.Set("X-Node-Id", nodeID)
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Nonce", nonce)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)))
	doRequest(t, req, want, out)
}

// doEnrollmentSignedJSON 发一个带接入签名的请求。
//
// 和 doSignedJSON 的区别在签的东西：那个用的是已建立身份的节点凭据，这个
// 用的是接入过程里那份临时凭据，签名还要带上接入 ID 和序号。两者不能混用
// ——面板在这两个阶段查的是不同的公钥。
func doEnrollmentSignedJSON(t *testing.T, privateKey ed25519.PrivateKey, enrollment nodefabric.EnrollmentOutput,
	method, target string, body []byte, want int, out any) {
	t.Helper()
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		t.Fatal(err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	ts := time.Now().UTC().Format(time.RFC3339)
	sum := sha256.Sum256(body)
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	payload := nodefabric.CanonicalEnrollmentRequestV1(method, req.URL.Path,
		enrollment.EnrollmentID, enrollment.NodeID, enrollment.Serial, ts, nonce, sum[:])
	req.Header.Set("X-Node-Id", enrollment.NodeID)
	req.Header.Set("X-Node-Serial", strconv.Itoa(enrollment.Serial))
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Nonce", nonce)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)))
	doRequest(t, req, want, out)
}

func doJSON(t *testing.T, method, target string, body []byte, headers map[string]string, want int, out any) {
	t.Helper()
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	doRequest(t, req, want, out)
}

func doRequest(t *testing.T, req *http.Request, want int, out any) {
	t.Helper()
	if req.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", req.Method, req.URL.Path, response.StatusCode, want, raw)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s %s: %v body=%s", req.Method, req.URL.Path, err, raw)
		}
	}
}
