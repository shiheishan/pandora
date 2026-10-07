package nodesim

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

type fakeNode struct {
	id, token  string
	priv       ed25519.PrivateKey
	releaseID  string
	generation uint64
	switched   map[string]bool
	stream     chan []byte
	streams    int
}

// fakeGateway 的计数都在 mu 下；测试读之前先 snapshot。
type fakeGateway struct {
	t      testing.TB
	srv    *httptest.Server
	tenant string
	// signer / svc 可被 rotateConfigKey 换掉，请求处理里经 keys() 在锁下取
	signer  *crypto.Signer
	svc     *nodefabric.Service
	log     *slog.Logger
	pullSec int
	pushSec int
	done    chan struct{}

	mu        sync.Mutex
	order     []string
	nodes     map[string]*fakeNode
	users     []nodefabric.ProxyUser
	nonces    map[string]bool
	hits      map[string]int
	sigOK     int
	sigFail   int
	authFail  int
	userINM   []string
	userCodes []int
	configINM []string
	// appliedHdr 是每次拉生效配置带的 X-Applied-Effective-Release（没带记空串），
	// effCodes 是对应的应答码（200 / 204）
	appliedHdr []string
	effCodes   []int
	phases     map[string]int
	// reject409 里的阶段一律回 409（面板明确拒收）
	reject409 map[string]bool
	beats     []nodefabric.HeartbeatInput
	pushes    int
	alives    int
	firstSeen map[string]time.Time
	fail500   map[string]bool
	badUsers  int
}

func newFakeGateway(t testing.TB, nodes, users, pullSec, pushSec int) *fakeGateway {
	t.Helper()
	signer, err := crypto.NewSigner(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGateway{
		t: t, tenant: uuid.NewString(), signer: signer, svc: nodefabric.NewService(nil, signer),
		log: slog.New(slog.DiscardHandler), pullSec: pullSec, pushSec: pushSec, done: make(chan struct{}),
		nodes: map[string]*fakeNode{}, nonces: map[string]bool{}, hits: map[string]int{},
		phases: map[string]int{}, firstSeen: map[string]time.Time{}, fail500: map[string]bool{},
		reject409: map[string]bool{},
	}
	for i := range nodes {
		seed := sha256.Sum256([]byte(fmt.Sprintf("fake-node-%d", i)))
		n := &fakeNode{id: uuid.NewString(), token: fmt.Sprintf("tok-%d", i),
			priv: ed25519.NewKeyFromSeed(seed[:]), switched: map[string]bool{}, stream: make(chan []byte, 16)}
		g.nodes[n.id] = n
		g.order = append(g.order, n.id)
	}
	for i := range users {
		g.users = append(g.users, nodefabric.ProxyUser{ID: int64(1000 + i), UUID: uuid.NewString(), DeviceLimit: 3})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/nodes/config-signing-key", g.signed(g.configKey))
	mux.HandleFunc("GET /v1/nodes/effective-config", g.signed(g.effectiveConfig))
	mux.HandleFunc("POST /v1/nodes/config/report", g.signed(g.report))
	mux.HandleFunc("POST /v1/nodes/heartbeat", g.signed(g.heartbeat))
	mux.HandleFunc("GET /api/v1/server/UniProxy/user", g.uni(g.uniUser))
	mux.HandleFunc("POST /api/v1/server/UniProxy/push", g.uni(g.uniPush))
	mux.HandleFunc("POST /api/v1/server/UniProxy/alive", g.uni(g.uniAlive))
	mux.HandleFunc("GET /api/v1/server/UniProxy/config", g.uni(g.uniConfig))
	mux.HandleFunc("POST /api/v1/server/UniProxy/status", g.uni(g.uniStatus))
	mux.HandleFunc("GET /api/v1/server/UniProxy/stream", g.uni(g.uniStream))
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		g.mu.Lock()
		g.hits[key]++
		id := r.Header.Get("X-Node-Id")
		if id == "" {
			id = r.URL.Query().Get("node_id")
		}
		if _, seen := g.firstSeen[id]; !seen {
			g.firstSeen[id] = time.Now()
		}
		fail := g.fail500[key]
		g.mu.Unlock()
		if fail {
			httpx.Fail(w, r, g.log, httpx.Internal(fmt.Errorf("injected failure")))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		close(g.done)
		g.srv.CloseClientConnections()
		g.srv.Close()
	})
	return g
}

// manifest 按 seed 的口径给出清单：节点私钥、运行令牌、配置签名公钥，用户带虚构来源 IP。
func (g *fakeGateway) manifest() *ltkit.Manifest {
	m := &ltkit.Manifest{Version: ltkit.ManifestVersion, Label: "test", TenantID: g.tenant}
	for _, id := range g.order {
		n := g.nodes[id]
		m.Nodes = append(m.Nodes, ltkit.ManifestNode{
			ID: n.id, NodeType: "vless", RuntimeToken: n.token, Serial: 1,
			PrivateKey:  base64.StdEncoding.EncodeToString(n.priv),
			ConfigKeyID: g.signer.KeyID(), ConfigPublicKey: base64.StdEncoding.EncodeToString(g.signer.PublicKey()),
		})
	}
	for i, u := range g.users {
		m.Users = append(m.Users, ltkit.ManifestUser{ID: u.UUID, RealIP: fmt.Sprintf("203.0.113.%d", 1+i%250)})
	}
	return m
}

func (g *fakeGateway) fail(w http.ResponseWriter, r *http.Request, sig bool) {
	g.mu.Lock()
	if sig {
		g.sigFail++
	} else {
		g.authFail++
	}
	g.mu.Unlock()
	httpx.Fail(w, r, g.log, httpx.New(httpx.CodeUnauthorized, "节点身份校验失败"))
}

// signed 是 api/node requireNodeSignature 的同规则实现，另加 nonce 防重放。
func (g *fakeGateway) signed(next func(http.ResponseWriter, *http.Request, *fakeNode)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeID, ts := r.Header.Get("X-Node-Id"), r.Header.Get("X-Node-Ts")
		nonce, sig := r.Header.Get("X-Node-Nonce"), r.Header.Get("X-Node-Sig")
		t, err := time.Parse(time.RFC3339, ts)
		if nodeID == "" || nonce == "" || sig == "" || err != nil || ts != t.UTC().Format(time.RFC3339) {
			g.fail(w, r, true)
			return
		}
		if d := time.Since(t); d > nodefabric.SignedRequestAcceptanceWindow || d < -nodefabric.SignedRequestAcceptanceWindow {
			g.fail(w, r, true)
			return
		}
		nonceRaw, err := nodefabric.DecodeNodeRequestNonce(nonce)
		body, rerr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		raw, derr := base64.StdEncoding.DecodeString(sig)
		g.mu.Lock()
		n := g.nodes[nodeID]
		g.mu.Unlock()
		if err != nil || rerr != nil || derr != nil || n == nil {
			g.fail(w, r, true)
			return
		}
		sum := sha256.Sum256(body)
		payload := nodefabric.CanonicalPayloadV2(r.Method, r.URL.Path, nodeID, ts, nonce, sum[:])
		if !crypto.Verify(n.priv.Public().(ed25519.PublicKey), payload, raw) {
			g.fail(w, r, true)
			return
		}
		g.mu.Lock()
		replay := g.nonces[nodeID+string(nonceRaw)]
		g.nonces[nodeID+string(nonceRaw)] = true
		if !replay {
			g.sigOK++
		}
		g.mu.Unlock()
		if replay {
			g.fail(w, r, true)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next(w, r, n)
	}
}

// keys 在锁下取当前的配置签名钥与面板服务（rotateConfigKey 会换掉它们）。
func (g *fakeGateway) keys() (*crypto.Signer, *nodefabric.Service) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.signer, g.svc
}

// rotateConfigKey 照面板的密钥轮换：换新签名钥，旧钥只用来给新公钥签过渡声明。
func (g *fakeGateway) rotateConfigKey() {
	next, err := crypto.NewSigner(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		g.t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	svc := nodefabric.NewService(nil, next)
	if err := svc.SetPreviousConfigSigner(g.signer); err != nil {
		g.t.Fatal(err)
	}
	g.signer, g.svc = next, svc
}

// configKey 照 api/node：钉住的就是当前钥回 204，钉的是上一把钥回过渡声明，否则 409。
func (g *fakeGateway) configKey(w http.ResponseWriter, r *http.Request, n *fakeNode) {
	_, svc := g.keys()
	t, err := svc.ConfigSigningKeyTransition(n.id, r.Header.Get("X-Config-Key-Id"), time.Now())
	if err != nil {
		httpx.Fail(w, r, g.log, err)
		return
	}
	if t == nil {
		httpx.NoContent(w)
		return
	}
	httpx.OK(w, t)
}

// effectiveConfig 用面板真实的 BuildNodeConfig 组装载荷、SignEffectiveRelease 签名；
// 只把 base_config 改成测试用的短节拍（真面板固定 pull 15 / push 60）。
// 节点带的已应用版本（面板同一个解析函数）仍是当前版时照 api/node 回 204。
func (g *fakeGateway) effectiveConfig(w http.ResponseWriter, r *http.Request, n *fakeNode) {
	hdr := r.Header.Get(nodefabric.AppliedEffectiveReleaseHeader)
	appliedID, appliedGen, ok := nodefabric.ParseAppliedEffectiveRelease(hdr)
	g.mu.Lock()
	if n.releaseID == "" {
		n.releaseID, n.generation = uuid.NewString(), 1
	}
	releaseID, generation := n.releaseID, n.generation
	unchanged := ok && appliedID == releaseID && appliedGen == generation
	g.appliedHdr = append(g.appliedHdr, hdr)
	if unchanged {
		g.effCodes = append(g.effCodes, http.StatusNoContent)
	} else {
		g.effCodes = append(g.effCodes, http.StatusOK)
	}
	g.mu.Unlock()
	if unchanged {
		w.Header().Set("Cache-Control", "no-store")
		httpx.NoContent(w)
		return
	}
	signer, _ := g.keys()
	payload := g.nodeConfig(n)
	manifest, _ := json.Marshal(map[string]any{"node": map[string]any{"id": n.id, "generation": generation}})
	content, msum := sha256.Sum256(payload), sha256.Sum256(manifest)
	issued := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	fields := nodefabric.EffectiveReleaseSignatureFields{TenantID: g.tenant, NodeID: n.id, ReleaseID: releaseID,
		Generation: generation, ContentHash: base64.StdEncoding.EncodeToString(content[:]),
		SourceManifestHash: base64.StdEncoding.EncodeToString(msum[:]), KeyID: signer.KeyID(),
		IssuedAt: issued, ExpiresAt: issued.Add(nodefabric.EffectiveReleaseMaxDeliveryWindow)}
	sig, err := nodefabric.SignEffectiveRelease(signer, fields)
	if err != nil {
		g.t.Error(err)
		return
	}
	httpx.OK(w, nodefabric.SignedConfig{ConfigContract: nodefabric.EffectiveReleaseContract, TenantID: g.tenant,
		NodeID: n.id, ReleaseID: releaseID, Generation: generation, Payload: payload,
		ContentSHA256: fields.ContentHash, Hash: fields.ContentHash, SourceManifest: manifest,
		SourceManifestSHA256: fields.SourceManifestHash, KeyID: fields.KeyID, IssuedAt: fields.IssuedAt,
		ExpiresAt: fields.ExpiresAt, Signature: sig})
}

// nodeConfig 是面板真实的 BuildNodeConfig 产物，只把 base_config 换成测试节拍。
func (g *fakeGateway) nodeConfig(n *fakeNode) []byte {
	_, svc := g.keys()
	body, _, err := svc.BuildNodeConfig(&nodefabric.ServingNode{ID: n.id, Name: "lt-node", NodeType: "vless", ServerPort: 443, Kernel: "auto"})
	if err != nil {
		g.t.Fatal(err)
	}
	var merged map[string]any
	_ = json.Unmarshal(body, &merged)
	merged["base_config"] = map[string]any{"pull_interval": g.pullSec, "push_interval": g.pushSec}
	payload, _ := json.Marshal(merged)
	return payload
}

// uniConfig 与 uniStatus 是兼容通道（节点没有签名身份时 pdnd 走它们）。
func (g *fakeGateway) uniConfig(w http.ResponseWriter, r *http.Request, n *fakeNode) {
	body := g.nodeConfig(n)
	sum := sha256.Sum256(body)
	etag := fmt.Sprintf(`"%x"`, sum[:8])
	g.mu.Lock()
	g.configINM = append(g.configINM, r.Header.Get("If-None-Match"))
	g.mu.Unlock()
	w.Header().Set("ETag", etag)
	if strings.TrimPrefix(r.Header.Get("If-None-Match"), "W/") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

func (g *fakeGateway) uniStatus(w http.ResponseWriter, r *http.Request, _ *fakeNode) {
	var in compatStatus
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Mem.Used > in.Mem.Total || in.CPU > 100 {
		httpx.Fail(w, r, g.log, httpx.New(httpx.CodeBadRequest, "状态上报数值非法"))
		return
	}
	httpx.OK(w, map[string]bool{"data": true})
}

// report 按面板规则：report_id 必须是 release_id 命名空间下阶段名的 UUIDv5，
// health_passed 之前必须见过 switched。
func (g *fakeGateway) report(w http.ResponseWriter, r *http.Request, n *fakeNode) {
	var in struct {
		Version       int    `json:"version"`
		ReportID      string `json:"report_id"`
		ReleaseID     string `json:"release_id"`
		Generation    uint64 `json:"generation"`
		ContentSHA256 string `json:"content_sha256"`
		Phase         string `json:"phase"`
		Detail        string `json:"detail"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, g.log, err)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reject409[in.Phase] || in.ReleaseID != n.releaseID ||
		in.ReportID != uuid.NewSHA1(uuid.MustParse(in.ReleaseID), []byte(in.Phase)).String() ||
		(in.Phase == "health_passed" && !n.switched[in.ReleaseID]) {
		httpx.Fail(w, r, g.log, httpx.New(httpx.CodeConflict, "report evidence mismatch"))
		return
	}
	if in.Phase == "switched" {
		n.switched[in.ReleaseID] = true
	}
	g.phases[in.Phase]++
	httpx.OK(w, map[string]bool{"ok": true})
}

func (g *fakeGateway) heartbeat(w http.ResponseWriter, r *http.Request, _ *fakeNode) {
	var in nodefabric.HeartbeatInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil { // 未知字段即 400，与面板一致
		httpx.Fail(w, r, g.log, err)
		return
	}
	g.mu.Lock()
	g.beats = append(g.beats, in)
	g.mu.Unlock()
	httpx.OK(w, nodefabric.HeartbeatOutput{NodeStatus: "active", IntervalSeconds: 30})
}

// uni 是 UniProxy 的令牌鉴权：Authorization 只认唯一一条 Bearer。
func (g *fakeGateway) uni(next func(http.ResponseWriter, *http.Request, *fakeNode)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		n := g.nodes[r.URL.Query().Get("node_id")]
		g.mu.Unlock()
		auth := r.Header.Values("Authorization")
		if n == nil || len(auth) != 1 || auth[0] != "Bearer "+n.token || r.URL.Query().Get("node_type") == "" {
			g.fail(w, r, false)
			return
		}
		next(w, r, n)
	}
}

func (g *fakeGateway) uniUser(w http.ResponseWriter, r *http.Request, _ *fakeNode) {
	g.mu.Lock()
	users := append([]nodefabric.ProxyUser(nil), g.users...)
	bad := g.badUsers > 0
	if bad {
		g.badUsers--
	}
	inm := r.Header.Get("If-None-Match")
	etag := nodefabric.UserSetVersion(users)
	code := http.StatusOK
	if !bad && inm != "" && strings.TrimPrefix(inm, "W/") == etag {
		code = http.StatusNotModified
	}
	g.userINM = append(g.userINM, inm)
	g.userCodes = append(g.userCodes, code)
	g.mu.Unlock()
	switch {
	case bad: // 一份解析不了的响应，带一个不该被记住的 ETag
		w.Header().Set("ETag", `"bad"`)
		_, _ = io.WriteString(w, `{"users":[`)
	case code == http.StatusNotModified:
		w.Header().Set("ETag", etag)
		w.WriteHeader(code)
	default:
		w.Header().Set("ETag", etag)
		httpx.OK(w, map[string]any{"users": users})
	}
}

func (g *fakeGateway) uniPush(w http.ResponseWriter, r *http.Request, _ *fakeNode) {
	var in map[string][2]int64
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in) == 0 {
		httpx.Fail(w, r, g.log, httpx.New(httpx.CodeBadRequest, "上报格式非法"))
		return
	}
	g.mu.Lock()
	g.pushes++
	g.mu.Unlock()
	httpx.OK(w, map[string]any{"data": true, "accepted": len(in)})
}

func (g *fakeGateway) uniAlive(w http.ResponseWriter, r *http.Request, _ *fakeNode) {
	var in map[string][]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in) == 0 {
		httpx.Fail(w, r, g.log, httpx.New(httpx.CodeBadRequest, "上报格式非法"))
		return
	}
	g.mu.Lock()
	g.alives++
	g.mu.Unlock()
	httpx.OK(w, map[string]any{"data": true})
}

// uniStream 照 api/node stream.go：先推一次全量用户，之后转发测试注入的事件，带注释帧保活。
func (g *fakeGateway) uniStream(w http.ResponseWriter, r *http.Request, n *fakeNode) {
	flusher := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	g.mu.Lock()
	n.streams++
	g.mu.Unlock()
	defer func() { g.mu.Lock(); n.streams--; g.mu.Unlock() }()
	g.pushUsersEvent(n.id)
	keepalive := time.NewTicker(200 * time.Millisecond)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-g.done:
			return
		case msg := <-n.stream:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", msg); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// pushEvent 用面板的信封给节点发一条事件。
func (g *fakeGateway) pushEvent(nodeID, event string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		g.t.Fatal(err)
	}
	msg, _ := json.Marshal(nodefabric.StreamMessage{Event: event, Data: raw, Timestamp: time.Now().UnixMilli()})
	g.mu.Lock()
	ch := g.nodes[nodeID].stream
	g.mu.Unlock()
	ch <- msg
}

func (g *fakeGateway) pushUsersEvent(nodeID string) string {
	g.mu.Lock()
	users := append([]nodefabric.ProxyUser(nil), g.users...)
	g.mu.Unlock()
	version := nodefabric.UserSetVersion(users)
	g.pushEvent(nodeID, nodefabric.EventSyncUsers, nodefabric.SyncUsersPayload{Users: users, Version: version})
	return version
}

func (g *fakeGateway) setUsers(users []nodefabric.ProxyUser) {
	g.mu.Lock()
	g.users = users
	g.mu.Unlock()
}

func (g *fakeGateway) bumpGeneration(nodeID string) {
	g.mu.Lock()
	n := g.nodes[nodeID]
	n.releaseID, n.generation = uuid.NewString(), n.generation+1
	g.mu.Unlock()
}

// waitFor 轮询条件直到成立或超时。
func (g *fakeGateway) waitFor(what string, timeout time.Duration, cond func() bool) {
	g.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		ok := cond()
		g.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.t.Fatalf("timed out waiting for %s", what)
}

// read 在锁下读计数。
func (g *fakeGateway) read(f func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f()
}
