package seed

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/crypto"
)

const (
	fakeNodeID       = "11111111-1111-7111-8111-111111111111"
	fakeEnrollmentID = "22222222-2222-7222-8222-222222222222"
)

// fakeNodeGateway 只做验签与最小响应，失败一律 401，与真网关同样不说明原因。
type fakeNodeGateway struct {
	t           *testing.T
	pub         ed25519.PublicKey
	runtimeHash string
	commits     int
	signedGets  int
	// realIPs 是接入两步（begin、commit）各自收到的 X-Real-IP
	realIPs []string
}

func (g *fakeNodeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	deny := func(why string) {
		g.t.Logf("fake gateway denied %s %s: %s", r.Method, r.URL.Path, why)
		w.WriteHeader(http.StatusUnauthorized)
	}
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if strings.HasPrefix(r.URL.Path, "/v1/nodes/enrollments") {
		g.realIPs = append(g.realIPs, r.Header.Get("X-Real-IP"))
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/nodes/enrollments":
		var req struct {
			PublicKey          string `json:"public_key"`
			RequestID          string `json:"request_id"`
			RuntimeTokenSHA256 string `json:"runtime_token_sha256"`
			NodeName           string `json:"node_name"`
		}
		if json.Unmarshal(body, &req) != nil {
			deny("bad body")
			return
		}
		pub, _ := base64.StdEncoding.DecodeString(req.PublicKey)
		sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Enrollment-Signature"))
		if len(pub) != ed25519.PublicKeySize ||
			!ed25519.Verify(pub, nodefabric.CanonicalEnrollmentBeginV1(r.URL.Path, req.RequestID, sum[:]), sig) {
			deny("begin proof")
			return
		}
		g.pub, g.runtimeHash = pub, req.RuntimeTokenSHA256
		reply(http.StatusCreated, map[string]any{"enrollment_id": fakeEnrollmentID, "node_id": fakeNodeID,
			"serial": 3, "state": "pending", "config_key_id": "kid", "config_public_key": "cHVi"})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/nodes/enrollments/"+fakeEnrollmentID+"/commit":
		serial, _ := strconv.Atoi(r.Header.Get("X-Node-Serial"))
		ts := r.Header.Get("X-Node-Ts")
		nonce := r.Header.Get("X-Node-Nonce")
		if parsed, err := time.Parse(time.RFC3339, ts); err != nil || ts != parsed.UTC().Format(time.RFC3339) {
			deny("timestamp not canonical")
			return
		}
		if _, err := nodefabric.DecodeNodeRequestNonce(nonce); err != nil {
			deny("nonce")
			return
		}
		sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Node-Sig"))
		payload := nodefabric.CanonicalEnrollmentRequestV1(r.Method, r.URL.Path, fakeEnrollmentID,
			r.Header.Get("X-Node-Id"), serial, ts, nonce, sum[:])
		if !crypto.Verify(g.pub, payload, sig) {
			deny("commit signature")
			return
		}
		g.commits++
		reply(http.StatusOK, map[string]any{"state": "committed"})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/nodes/effective-config":
		ts, nonce := r.Header.Get("X-Node-Ts"), r.Header.Get("X-Node-Nonce")
		sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Node-Sig"))
		if !crypto.Verify(g.pub, nodefabric.CanonicalPayloadV2(r.Method, r.URL.Path, r.Header.Get("X-Node-Id"), ts, nonce, sum[:]), sig) {
			deny("node signature")
			return
		}
		g.signedGets++
		reply(http.StatusOK, map[string]any{"release_id": "r1"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestEnrollAndSignedGetPassGatewayVerification(t *testing.T) {
	gw := &fakeNodeGateway{t: t}
	srv := httptest.NewServer(gw)
	defer srv.Close()
	nc := newNodeClient(srv.URL, "loadtest", strings.Repeat("1", 64))
	nc.ipHeaders = []string{"X-Real-IP"}
	id, err := newNodeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := nc.enroll(ctx, id, "loadtest-ci-abc123-n0001", "bootstrap", fakeNodeID, "lt-host-0001", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	// 接入两步都带节点所在服务器的虚构地址，nginx 的每 IP 限流才按节点各算各的
	if len(gw.realIPs) != 2 || gw.realIPs[0] != "203.0.113.7" || gw.realIPs[1] != "203.0.113.7" {
		t.Fatalf("enrollment X-Real-IP = %v, want the node's server address on both steps", gw.realIPs)
	}
	if gw.commits != 1 || res.Serial != 3 || res.ConfigKeyID != "kid" || res.ConfigPublicKey != "cHVi" {
		t.Fatalf("unexpected enrollment result %+v (commits %d)", res, gw.commits)
	}
	// 面板只拿到运行令牌的 sha256（标准 base64），manifest 里的私钥能推出登记的公钥
	want := sha256.Sum256([]byte(id.RuntimeToken))
	if gw.runtimeHash != base64.StdEncoding.EncodeToString(want[:]) {
		t.Fatal("runtime token hash is not the canonical base64 SHA-256 of the token")
	}
	if strings.Contains(gw.runtimeHash, id.RuntimeToken) || len(id.RuntimeToken) != 43 {
		t.Fatalf("runtime token must be 32 random bytes in raw base64url, got length %d", len(id.RuntimeToken))
	}
	if !id.PrivateKey.Public().(ed25519.PublicKey).Equal(gw.pub) {
		t.Fatal("manifest private key does not match the enrolled public key")
	}
	if _, err := nc.signedGet(ctx, id, fakeNodeID, "/v1/nodes/effective-config"); err != nil || gw.signedGets != 1 {
		t.Fatalf("signed GET failed: %v", err)
	}
}

func TestEnrollRefusesForeignNode(t *testing.T) {
	srv := httptest.NewServer(&fakeNodeGateway{t: t})
	defer srv.Close()
	id, _ := newNodeIdentity()
	_, err := newNodeClient(srv.URL, "loadtest", strings.Repeat("1", 64)).
		enroll(context.Background(), id, "n", "bootstrap", "33333333-3333-7333-8333-333333333333", "h", "")
	if err == nil || !strings.Contains(err.Error(), "landed on node") {
		t.Fatalf("enrollment that lands on another node must fail, got %v", err)
	}
}

func TestUniProxyUserSet(t *testing.T) {
	want := map[int64]bool{1: true, 2: true, 3: true}
	plain := jsonObject{"users": []any{map[string]any{"id": 1.0}, map[string]any{"id": 2.0}, map[string]any{"id": 3.0}}}
	wrapped := jsonObject{"data": map[string]any{"users": plain["users"]}}
	for _, resp := range []jsonObject{plain, wrapped} {
		ids, err := uniProxyUserIDs(resp)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkUserSet(ids, want); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := uniProxyUserIDs(jsonObject{"nope": 1}); err == nil {
		t.Fatal("missing users array must fail")
	}
	for _, bad := range [][]int64{{1, 2}, {1, 2, 3, 4}, {1, 2, 2, 3}} {
		if err := checkUserSet(bad, want); err == nil {
			t.Fatalf("checkUserSet(%v) should fail", bad)
		}
	}
}
