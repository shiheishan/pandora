// [INPUT]: 只依赖标准库（crypto/ed25519、net/http）；协议口径对齐 pdnd/panel 的 signed.go、effective_release.go 与面板 api/node 路由
// [OUTPUT]: 可执行的回环模拟面板：UniProxy 兼容通道 + 签名通道（身份文件、effective release 签发、心跳、配置上报），并把观测写进状态文件
// [POS]: pdnd/release 的验收夹具，只被 runtime-acceptance.sh 调用；main_test.go 用 pdnd 自己的签名客户端校验它签出的东西，保证夹具与节点协议不漂移
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Command acceptancepanel 是 runtime-acceptance.sh 的模拟面板。
//
// 为什么是 Go 而不是 Python：签名通道要 Ed25519，Python 标准库没有；
// 在发布门禁里塞一份纯 Python 椭圆曲线实现，出了错比被测对象还难查。
// Go 本来就是构建前提，交叉编译一下就能带到没有 Go 的验收机上。
//
// 签名通道的原像与校验规则按面板实现独立重写一遍，而不是 import
// pdnd/panel：夹具借用被测代码的实现，两边一起错时验收照样是绿的。
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const effectiveReleaseContract = "aegis-node-effective-config-release-v1"

// State 是脚本在验收结束时读取的观测结果。
type State struct {
	Mode                   string   `json:"mode"`
	UniProxyConfigHits     int      `json:"uniproxy_config_hits"`
	UniProxyStatusReports  int      `json:"uniproxy_status_reports"`
	EffectiveConfigFetches int      `json:"effective_config_fetches"`
	ReportPhases           []string `json:"report_phases"`
	Heartbeats             int      `json:"heartbeats"`
	HeartbeatsWithMetrics  int      `json:"heartbeats_with_metrics"`
	BadSignatures          int      `json:"bad_signatures"`
}

type identity struct {
	Server          string `json:"server"`
	NodeID          string `json:"node_id"`
	Serial          int    `json:"serial"`
	PrivateKey      string `json:"private_key"`
	ConfigPublicKey string `json:"config_public_key"`
	ConfigKeyID     string `json:"config_key_id"`
	RuntimeToken    string `json:"runtime_token"`
}

type panel struct {
	nodePort  int
	statePath string

	// 签名通道：nodePub 验节点请求，configPriv 签下发的配置
	nodeID     string
	nodePub    ed25519.PublicKey
	configPriv ed25519.PrivateKey
	keyID      string
	tenantID   string
	releaseID  string

	mu    sync.Mutex
	state State
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "listen address")
	nodePort := flag.Int("node-port", 0, "inbound port handed to the node")
	statePath := flag.String("state", "", "where to write observed events (JSON)")
	identityPath := flag.String("identity", "", "signed mode: write a node identity here")
	nodeID := flag.String("node-id", "", "signed mode: canonical lowercase UUID of the node")
	flag.Parse()
	if *nodePort <= 0 || *statePath == "" {
		log.Fatal("-node-port and -state are required")
	}

	p := &panel{nodePort: *nodePort, statePath: *statePath, state: State{Mode: "compat", ReportPhases: []string{}}}
	if *identityPath != "" {
		id, err := p.enableSigned(*nodeID, "http://"+*listen)
		if err != nil {
			log.Fatal(err)
		}
		raw, _ := json.Marshal(id)
		if err := os.WriteFile(*identityPath, raw, 0o600); err != nil {
			log.Fatal(err)
		}
	}
	p.save()
	log.Fatal(http.ListenAndServe(*listen, p))
}

// enableSigned 生成节点密钥与配置签名密钥，返回要写给节点的身份。
func (p *panel) enableSigned(nodeID, server string) (*identity, error) {
	if !canonicalUUID(nodeID) {
		return nil, fmt.Errorf("-node-id must be a canonical lowercase UUID, got %q", nodeID)
	}
	nodePub, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	configPub, configPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	keySum := sha256.Sum256(configPub)
	p.nodeID, p.nodePub, p.configPriv = nodeID, nodePub, configPriv
	p.keyID = base64.RawURLEncoding.EncodeToString(keySum[:8])
	p.tenantID, p.releaseID = newUUID(), newUUID()
	p.state.Mode = "signed"
	return &identity{
		Server: server, NodeID: nodeID, Serial: 1,
		PrivateKey:      base64.StdEncoding.EncodeToString(nodePriv),
		ConfigPublicKey: base64.StdEncoding.EncodeToString(configPub),
		ConfigKeyID:     p.keyID,
		RuntimeToken:    "acceptance-runtime-token",
	}, nil
}

func (p *panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/api/v1/server/UniProxy/"):
		p.uniProxy(w, r, strings.TrimPrefix(path, "/api/v1/server/UniProxy/"))
	case strings.HasPrefix(path, "/v1/nodes/"):
		if p.nodePub == nil || !p.verifyNodeRequest(r, body) {
			p.record(func(s *State) { s.BadSignatures++ })
			http.Error(w, `{"error":"invalid node signature"}`, http.StatusUnauthorized)
			return
		}
		p.signed(w, r, path, body)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

//------------------------------------------------------------------------------
// UniProxy 兼容通道：配置只在兼容模式下发，用户表两种模式都要（节点的用户
// 同步、流量与在线上报目前只走这条通道）
//------------------------------------------------------------------------------

func (p *panel) uniProxy(w http.ResponseWriter, r *http.Request, name string) {
	switch {
	case r.Method == http.MethodGet && name == "config":
		p.record(func(s *State) { s.UniProxyConfigHits++ })
		if p.nodePub != nil {
			// 签名模式下节点不该来这里拿配置：拒掉并计数，真走错了路径，
			// 入站起不来、状态里的计数也会暴露出来。
			http.Error(w, `{"error":"config is served over the signed channel"}`, http.StatusGone)
			return
		}
		w.Header().Set("ETag", `"runtime-v1"`)
		writeJSON(w, p.nodeConfig())
	case r.Method == http.MethodGet && name == "user":
		writeJSON(w, map[string]any{"users": []any{}})
	case r.Method == http.MethodPost:
		if name == "status" {
			p.record(func(s *State) { s.UniProxyStatusReports++ })
		}
		w.WriteHeader(http.StatusOK)
	default:
		// stream 等其余接口：告诉节点「没有」，它会退避重连，不影响验收
		w.WriteHeader(http.StatusNoContent)
	}
}

func (p *panel) nodeConfig() map[string]any {
	return map[string]any{
		"protocol": "socks", "server_port": p.nodePort,
		"base_config": map[string]any{"pull_interval": 1, "push_interval": 1},
	}
}

//------------------------------------------------------------------------------
// 签名通道
//------------------------------------------------------------------------------

func (p *panel) signed(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	switch {
	case r.Method == http.MethodGet && path == "/v1/nodes/config-signing-key":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && path == "/v1/nodes/effective-config":
		cfg, err := p.effectiveRelease(time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		p.record(func(s *State) { s.EffectiveConfigFetches++ })
		writeJSON(w, cfg)
	case r.Method == http.MethodPost && path == "/v1/nodes/config/report":
		var in struct {
			ReleaseID string `json:"release_id"`
			Phase     string `json:"phase"`
			Detail    string `json:"detail"`
		}
		_ = json.Unmarshal(body, &in)
		if in.ReleaseID != p.releaseID {
			http.Error(w, `{"error":"unknown release"}`, http.StatusConflict)
			return
		}
		p.record(func(s *State) { s.ReportPhases = append(s.ReportPhases, in.Phase) })
		if in.Detail != "" {
			log.Printf("config report %s: %s", in.Phase, in.Detail)
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && path == "/v1/nodes/heartbeat":
		var in struct {
			Metrics json.RawMessage `json:"metrics"`
		}
		_ = json.Unmarshal(body, &in)
		withMetrics := len(in.Metrics) > 0 && string(in.Metrics) != "null"
		p.record(func(s *State) {
			s.Heartbeats++
			if withMetrics {
				s.HeartbeatsWithMetrics++
			}
		})
		writeJSON(w, map[string]any{"node_status": "active", "interval_seconds": 30})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// verifyNodeRequest 按面板 requireNodeSignature 的 V2 原像验节点签名。
func (p *panel) verifyNodeRequest(r *http.Request, body []byte) bool {
	if r.Header.Get("X-Node-Id") != p.nodeID {
		return false
	}
	ts, err := time.Parse(time.RFC3339, r.Header.Get("X-Node-Ts"))
	if err != nil || time.Since(ts).Abs() > 5*time.Minute {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Node-Sig"))
	if err != nil {
		return false
	}
	sum := sha256.Sum256(body)
	payload := "PANDORA-NODE-REQUEST-V2\n" + r.Method + "\n" + r.URL.Path + "\n" + p.nodeID + "\n" +
		r.Header.Get("X-Node-Ts") + "\n" + r.Header.Get("X-Node-Nonce") + "\n" + base64.StdEncoding.EncodeToString(sum[:])
	return ed25519.Verify(p.nodePub, []byte(payload), sig)
}

// effectiveRelease 签发一份 effective release，原像与面板 effective_release_codec 同构。
func (p *panel) effectiveRelease(now time.Time) (map[string]any, error) {
	payload, err := json.Marshal(p.nodeConfig())
	if err != nil {
		return nil, err
	}
	manifest := []byte(`{"sources":["runtime-acceptance"]}`)
	contentSum, manifestSum := sha256.Sum256(payload), sha256.Sum256(manifest)
	content := base64.StdEncoding.EncodeToString(contentSum[:])
	manifestHash := base64.StdEncoding.EncodeToString(manifestSum[:])
	issued := now.UTC().Truncate(time.Microsecond)
	expires := issued.Add(5 * time.Minute)
	preimage := effectiveReleaseContract + "\n" +
		"tenant_id=" + p.tenantID + "\n" +
		"node_id=" + p.nodeID + "\n" +
		"release_id=" + p.releaseID + "\n" +
		"generation=" + strconv.Itoa(1) + "\n" +
		"content_sha256=" + content + "\n" +
		"source_manifest_sha256=" + manifestHash + "\n" +
		"key_id=" + p.keyID + "\n" +
		"issued_at=" + issued.Format(time.RFC3339Nano) + "\n" +
		"expires_at=" + expires.Format(time.RFC3339Nano) + "\n"
	return map[string]any{
		"config_contract": effectiveReleaseContract,
		"tenant_id":       p.tenantID, "node_id": p.nodeID, "release_id": p.releaseID, "generation": 1,
		"content_sha256": content, "hash": content,
		"source_manifest": json.RawMessage(manifest), "source_manifest_sha256": manifestHash,
		"issued_at": issued, "expires_at": expires, "key_id": p.keyID,
		"payload":   json.RawMessage(payload),
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(p.configPriv, []byte(preimage))),
	}, nil
}

//------------------------------------------------------------------------------
// 杂项
//------------------------------------------------------------------------------

// record 改状态并立即落盘：脚本随时可能读，也可能在任意时刻杀掉本进程。
func (p *panel) record(change func(*State)) {
	p.mu.Lock()
	change(&p.state)
	p.mu.Unlock()
	p.save()
}

func (p *panel) save() {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw, _ := json.Marshal(p.state)
	tmp := p.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, p.statePath)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func canonicalUUID(s string) bool {
	if len(s) != 36 || strings.ToLower(s) != s || s == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
	}
	return true
}
