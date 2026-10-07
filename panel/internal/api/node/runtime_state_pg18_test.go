package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

// checkRuntimeStatePG18 在一个已接入的节点上核对 pdnd 上报的真实运行状态（w4deliver）：
//   - 签名心跳：正文 runtime_status + 请求头 X-Node-Runtime-Reason 落进节点行，degraded 降到 40；
//     同样的状态再报一次 runtime_state_at 不动（只在变化时前进）；恢复 running 清掉原因；
//   - 兼容通道 /status：两个请求头同样落库，不带头的老节点照旧 90、状态为空；
//   - UniProxy push 带 X-Report-Id：同一编号重发只留档、不入账。
func checkRuntimeStatePG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, privateKey ed25519.PrivateKey,
	tenantID, nodeID, serverURL, runtimeToken string) {
	t.Helper()
	type state struct {
		status, reason *string
		score          int
		at             *time.Time
	}
	read := func() (s state) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT runtime_status, runtime_reason, health_score, runtime_state_at
			FROM nodes WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, nodeID).Scan(&s.status, &s.reason, &s.score, &s.at); err != nil {
			t.Fatal(err)
		}
		return s
	}
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	beat := func(status, reason string) {
		t.Helper()
		body := mustJSON(t, map[string]any{"agent_version": "e2e-test", "runtime_version": "native-e2e", "runtime_status": status})
		req := signedRequestWithHeaders(t, privateKey, nodeID, http.MethodPost, serverURL+"/v1/nodes/heartbeat", body)
		if reason != "" {
			req.Header.Set(nodefabric.RuntimeReasonHeader, reason)
		}
		doRequest(t, req, http.StatusOK, nil)
	}

	beat("degraded", "port_in_use:443/tcp:other")
	first := read()
	if str(first.status) != "degraded" || str(first.reason) != "port_in_use:443/tcp:other" || first.score != 40 || first.at == nil {
		t.Fatalf("degraded heartbeat stored %s / %s / %d / %v", str(first.status), str(first.reason), first.score, first.at)
	}
	time.Sleep(20 * time.Millisecond)
	beat("degraded", "port_in_use:443/tcp:other")
	if again := read(); again.at == nil || !again.at.Equal(*first.at) {
		t.Fatalf("unchanged runtime state moved runtime_state_at %v → %v", first.at, again.at)
	}
	beat("running", "")
	recovered := read()
	if str(recovered.status) != "running" || recovered.reason != nil || recovered.score != 90 ||
		recovered.at == nil || !recovered.at.After(*first.at) {
		t.Fatalf("recovered heartbeat stored %s / %s / %d / %v", str(recovered.status), str(recovered.reason), recovered.score, recovered.at)
	}

	uniURL := func(path string) string {
		return fmt.Sprintf("%s/api/v1/server/UniProxy/%s?node_id=%s&node_type=shadowsocks", serverURL, path, nodeID)
	}
	status := func(headers map[string]string) {
		t.Helper()
		h := map[string]string{"Authorization": "Bearer " + runtimeToken}
		for k, v := range headers {
			h[k] = v
		}
		doJSON(t, http.MethodPost, uniURL("status"),
			[]byte(`{"cpu":1.5,"mem":{"total":2048,"used":1024},"swap":{"total":0,"used":0},"disk":{"total":4096,"used":1024}}`),
			h, http.StatusOK, nil)
	}
	status(map[string]string{nodefabric.RuntimeStatusHeader: "degraded", nodefabric.RuntimeReasonHeader: "not_started"})
	if s := read(); str(s.status) != "degraded" || str(s.reason) != "not_started" || s.score != 40 {
		t.Fatalf("compat degraded status stored %s / %s / %d", str(s.status), str(s.reason), s.score)
	}
	status(nil)
	if s := read(); s.status != nil || s.reason != nil || s.score != 90 {
		t.Fatalf("legacy compat status stored %s / %s / %d, want no runtime state and 90", str(s.status), str(s.reason), s.score)
	}

	const reportID = "8c3e9d6a-4b2f-4e1a-9c7d-6b5a4f3e2d1c"
	push := map[string]string{"Authorization": "Bearer " + runtimeToken, nodefabric.TrafficReportIDHeader: reportID}
	doJSON(t, http.MethodPost, uniURL("push"), []byte(`{"900000001":[1,1]}`), push, http.StatusOK, nil)
	doJSON(t, http.MethodPost, uniURL("push"), []byte(`{"900000001":[1,1]}`), push, http.StatusOK, nil)
	var originals, duplicates int
	if err := admin.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE client_report_id = $3),
		count(*) FILTER (WHERE duplicate_of = (SELECT id FROM node_traffic_reports
		                                        WHERE node_id=$2::uuid AND client_report_id=$3))
		FROM node_traffic_reports WHERE tenant_id=$1 AND node_id=$2::uuid`, tenantID, nodeID, reportID).
		Scan(&originals, &duplicates); err != nil {
		t.Fatal(err)
	}
	if originals != 1 || duplicates != 1 {
		t.Fatalf("report id push: originals=%d duplicates=%d, want 1/1", originals, duplicates)
	}
}

// signedRequestWithHeaders 与 doSignedJSON 签同样的原像，但把请求交回调用方再加请求头
// （X-Node-Runtime-Reason 不进签名原像）。
func signedRequestWithHeaders(t *testing.T, privateKey ed25519.PrivateKey, nodeID, method, target string, body []byte) *http.Request {
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", nodeID)
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Nonce", nonce)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)))
	return req
}
