package nodesim

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// pdnd 的 SignedClient 把超时写死为 15 秒，不跟 panel.timeout_seconds 走。
const signedTimeout = 15 * time.Second

const (
	pathConfigKey       = "/v1/nodes/config-signing-key"
	pathEffectiveConfig = "/v1/nodes/effective-config"
	pathConfigReport    = "/v1/nodes/config/report"
	pathHeartbeat       = "/v1/nodes/heartbeat"
)

// canonicalServer 与 pdnd validateSignedServer 同一规则：https，或只在回环上
// 允许 http。pdnd 遇到不合规的地址会拒绝签名通道，模拟器直接报错退出，
// 不悄悄退到兼容通道——那样测出来的就不是签名通道的负载了。
func canonicalServer(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid node gateway URL %q", raw)
	}
	if u.Scheme == "https" {
		return base, nil
	}
	host := u.Hostname()
	if u.Scheme == "http" && (strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return base, nil
	}
	return "", fmt.Errorf("node gateway URL must use HTTPS (HTTP is allowed only on loopback), as pdnd requires")
}

type signedClient struct {
	base   string
	nodeID string
	signer *crypto.Signer
	// 钉住的配置签名公钥，可被密钥轮换声明替换
	configKeyID string
	configPub   ed25519.PublicKey
	verify      bool
	http        *http.Client
	obs         *observer
	// 换钥检查节拍（current，对应 pdnd keyCheckedAt / keyCheckWait）：keyCheckEvery 为 0
	// 时每次拉配置前都问（legacy）；keyJitter 给每次的间隔加抖动，nil 表示不抖。
	keyCheckEvery time.Duration
	keyCheckedAt  time.Time
	keyCheckWait  time.Duration
	keyJitter     func(time.Duration) time.Duration
}

// statusError 是签名请求的非 2xx 应答，对应 pdnd panel.StatusError：current 按状态码
// 判断回执是被面板收下、明确拒收，还是暂时没送到（reportSettled）。
type statusError struct {
	method, path string
	code         int
	body         string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.method, e.path, e.code, e.body)
}

func newSignedClient(base string, n ltkit.ManifestNode, verify bool, obs *observer) (*signedClient, error) {
	priv, err := base64.StdEncoding.DecodeString(n.PrivateKey)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("node %s: invalid private key in manifest", n.ID)
	}
	signer, err := crypto.NewSigner(priv[:ed25519.SeedSize])
	if err != nil {
		return nil, err
	}
	pub, err := base64.StdEncoding.DecodeString(n.ConfigPublicKey)
	if verify && (err != nil || len(pub) != ed25519.PublicKeySize) {
		return nil, fmt.Errorf("node %s: invalid config public key in manifest", n.ID)
	}
	// pdnd 的签名客户端用 http.DefaultTransport（一台机器一个），模拟器每个
	// 节点克隆一份：两百台机器就是两百个独立的连接池，面板看到的连接数才对。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &signedClient{
		base: base, nodeID: n.ID, signer: signer,
		configKeyID: n.ConfigKeyID, configPub: pub, verify: verify, obs: obs,
		http: &http.Client{Timeout: signedTimeout, Transport: withRealIP(transport, n.RealIP),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

// do 发一个签名请求，口径与 pdnd SignedClient.Do 一致：
// 秒级 UTC 时间戳、16 字节随机 nonce（base64url 无填充）、请求体 SHA-256，
// 规范串由 nodefabric.CanonicalPayloadV2 生成，Ed25519 签名。
// 状态码 ≥ 300 一律算失败（*statusError）；401 记 sig_fail。
func (c *signedClient) do(ctx context.Context, method, path string, body []byte, out any, flag string) error {
	_, err := c.send(ctx, method, path, body, nil, out, flag)
	return err
}

// send 是 do 的本体，另返回 2xx 的状态码；headers 是额外的请求头，与 pdnd 一样不进
// 签名原像。生效配置回 204 记 unchanged_204。
func (c *signedClient) send(ctx context.Context, method, path string, body []byte, headers map[string]string, out any, flag string) (int, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		return 0, fmt.Errorf("generate request nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	sum := sha256.Sum256(body)
	sig := c.signer.Sign(nodefabric.CanonicalPayloadV2(method, path, c.nodeID, ts, nonce, sum[:]))
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", c.nodeID)
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Nonce", nonce)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(sig))
	if path == pathConfigKey {
		req.Header.Set("X-Config-Key-Id", c.configKeyID)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	endpoint := "node:" + method + " " + path
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.obs.record(ctx, endpoint, 0, time.Since(start), err, flag)
		return 0, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		flag = flagSigFail
	case resp.StatusCode == http.StatusNoContent && path == pathEffectiveConfig:
		flag = flagUnchanged
	}
	c.obs.record(ctx, endpoint, resp.StatusCode, time.Since(start), nil, flag)
	if resp.StatusCode >= 300 {
		return resp.StatusCode, &statusError{method: method, path: path, code: resp.StatusCode, body: strings.TrimSpace(string(raw))}
	}
	if out != nil && len(raw) > 0 {
		return resp.StatusCode, json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

// config 复刻老 pdnd SignedClient.Config（legacy）：每次先问一遍密钥轮换，再拉全量有效配置。
func (c *signedClient) config(ctx context.Context, flag string) (*nodefabric.SignedConfig, error) {
	if err := c.refreshConfigKey(ctx, flag); err != nil {
		return nil, err
	}
	var out nodefabric.SignedConfig
	if err := c.do(ctx, http.MethodGet, pathEffectiveConfig, nil, &out, flag); err != nil {
		return nil, err
	}
	return &out, nil
}

// configSince 复刻新 pdnd SignedClient.ConfigSince（current）：换钥检查到点才做；
// applied（"<release_id>/<generation>"）非空时带 X-Applied-Effective-Release，
// 面板回 204 即仍是当前版，返回 unchanged=true、cfg 为 nil。没带这个头却回 204
// 是面板出错，与 pdnd 一样报错。
func (c *signedClient) configSince(ctx context.Context, applied, flag string) (cfg *nodefabric.SignedConfig, unchanged bool, err error) {
	if err := c.refreshConfigKeyIfDue(ctx, flag); err != nil {
		return nil, false, err
	}
	var headers map[string]string
	if applied != "" {
		headers = map[string]string{nodefabric.AppliedEffectiveReleaseHeader: applied}
	}
	var out nodefabric.SignedConfig
	status, err := c.send(ctx, http.MethodGet, pathEffectiveConfig, nil, headers, &out, flag)
	if err != nil {
		return nil, false, err
	}
	if status == http.StatusNoContent {
		if headers == nil {
			return nil, false, errors.New("panel returned no effective config")
		}
		return nil, true, nil
	}
	return &out, false, nil
}

// refreshConfigKeyIfDue 对应 pdnd refreshConfigSigningKeyIfDue：上次成功检查之后
// 不满 keyCheckWait 就不问。keyCheckEvery 为 0（legacy）时每次都问。
func (c *signedClient) refreshConfigKeyIfDue(ctx context.Context, flag string) error {
	if c.keyCheckEvery > 0 && !c.keyCheckedAt.IsZero() && time.Since(c.keyCheckedAt) < c.keyCheckWait {
		return nil
	}
	return c.refreshConfigKey(ctx, flag)
}

// markConfigKeyChecked 记下一次成功的换钥检查，下一次的间隔带抖动（pdnd markConfigKeyChecked）。
func (c *signedClient) markConfigKeyChecked() {
	c.keyCheckedAt = time.Now()
	c.keyCheckWait = c.keyCheckEvery
	if c.keyJitter != nil {
		c.keyCheckWait = c.keyJitter(c.keyCheckEvery)
	}
}

// refreshConfigKey 面板回 204 表示钉住的公钥仍是当前公钥；回了过渡声明就换钥。
//
// 过渡声明的签名没有在这里验：面板侧的 verifyConfigKeyTransition 未导出，
// 自己再写一份规范串正是要避免的事。只核对节点与旧钥 ID 后采纳，并记一个
// key_transition 标签——压测期间面板不该轮换密钥，看到它就说明环境不对。
func (c *signedClient) refreshConfigKey(ctx context.Context, flag string) error {
	var t nodefabric.ConfigKeyTransition
	if err := c.do(ctx, http.MethodGet, pathConfigKey, nil, &t, flag); err != nil {
		return fmt.Errorf("refresh config signing key: %w", err)
	}
	if t.Contract == "" {
		c.markConfigKeyChecked()
		return nil
	}
	c.obs.fleet.keyTransitions.Add(1)
	if t.NodeID != c.nodeID || t.FromKeyID != c.configKeyID {
		return errors.New("config key transition identity mismatch")
	}
	pub, err := base64.StdEncoding.DecodeString(t.ToPublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("config key transition carries an invalid public key")
	}
	c.configKeyID, c.configPub = t.ToKeyID, pub
	c.markConfigKeyChecked()
	return nil
}

// verifyConfig 是 pdnd VerifyConfig 的同口径实现，验签本身调面板侧现成函数：
// 有效发布物走 VerifyEffectiveReleaseSignature（含钥 ID 与公钥指纹、时间窗），
// 旧版签名配置走 VerifyConfigSignature。哈希与节点身份这几项面板函数不管，
// 按 pdnd 的顺序在这里补齐。
func (c *signedClient) verifyConfig(cfg *nodefabric.SignedConfig) error {
	if !c.verify {
		return nil
	}
	if cfg == nil || len(cfg.Payload) == 0 {
		return errors.New("empty signed config")
	}
	if c.configKeyID == "" || cfg.KeyID != c.configKeyID {
		return errors.New("config key id mismatch")
	}
	if cfg.ConfigContract == "" {
		sum := sha256.Sum256(cfg.Payload)
		if base64.StdEncoding.EncodeToString(sum[:]) != cfg.Hash {
			return errors.New("config hash mismatch")
		}
		if !nodefabric.VerifyConfigSignature(c.configPub, cfg.Hash, cfg.Signature, cfg.ExpiresAt) {
			return errors.New("config signature invalid or expired")
		}
		return nil
	}
	if cfg.ConfigContract != nodefabric.EffectiveReleaseContract {
		return errors.New("unsupported effective config contract")
	}
	if cfg.NodeID != c.nodeID {
		return errors.New("effective config node identity mismatch")
	}
	content := sha256.Sum256(cfg.Payload)
	if base64.StdEncoding.EncodeToString(content[:]) != cfg.ContentSHA256 || cfg.Hash != cfg.ContentSHA256 {
		return errors.New("effective config content hash mismatch")
	}
	manifest := sha256.Sum256(cfg.SourceManifest)
	if base64.StdEncoding.EncodeToString(manifest[:]) != cfg.SourceManifestSHA256 {
		return errors.New("effective config source manifest hash mismatch")
	}
	return nodefabric.VerifyEffectiveReleaseSignature(c.configPub, c.configKeyID, nodefabric.EffectiveReleaseSignatureFields{
		TenantID: cfg.TenantID, NodeID: cfg.NodeID, ReleaseID: cfg.ReleaseID, Generation: cfg.Generation,
		ContentHash: cfg.ContentSHA256, SourceManifestHash: cfg.SourceManifestSHA256, KeyID: cfg.KeyID,
		IssuedAt: cfg.IssuedAt, ExpiresAt: cfg.ExpiresAt,
	}, cfg.Signature, time.Now())
}

// reportPhase 复刻 pdnd reportSignedConfigPhase：有效发布物带确定性 report_id
// （release_id 为命名空间、阶段名为名字的 UUIDv5），重放同一阶段在面板是幂等的；
// 旧版配置只带版本号。
func (c *signedClient) reportPhase(ctx context.Context, cfg *nodefabric.SignedConfig, phase, detail string) error {
	var body []byte
	var err error
	if cfg.ConfigContract != "" {
		releaseID, perr := uuid.Parse(cfg.ReleaseID)
		if perr != nil || releaseID == uuid.Nil || releaseID.String() != cfg.ReleaseID {
			return errors.New("effective release id must be a non-zero canonical UUID")
		}
		body, err = json.Marshal(map[string]any{
			"report_id": uuid.NewSHA1(releaseID, []byte(phase)).String(), "release_id": cfg.ReleaseID,
			"generation": cfg.Generation, "content_sha256": cfg.ContentSHA256,
			"phase": phase, "detail": detail,
		})
	} else {
		body, err = json.Marshal(map[string]any{"version": cfg.Version, "phase": phase, "detail": detail})
	}
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, pathConfigReport, body, nil, "phase:"+phase)
}

// heartbeatBody 是 pdnd HeartbeatInput 的线格式：生效发布物三项与 metrics
// 带 omitempty，和面板侧结构体（全不省略）的字节不同，所以不直接用面板类型。
// 面板以 DisallowUnknownFields 解码，这里每个键都必须是面板认识的。
type heartbeatBody struct {
	AgentVersion         string              `json:"agent_version"`
	RuntimeVersion       string              `json:"runtime_version"`
	ConfigSigningKeyID   string              `json:"config_signing_key_id"`
	ConfigVersion        int                 `json:"applied_config_version"`
	ConfigHash           string              `json:"applied_config_hash"`
	AppliedReleaseID     string              `json:"applied_effective_release_id,omitempty"`
	AppliedGeneration    uint64              `json:"applied_effective_generation,omitempty"`
	AppliedContentSHA256 string              `json:"applied_effective_content_sha256,omitempty"`
	CPUCores             int                 `json:"cpu_cores"`
	MemoryMB             int                 `json:"memory_mb"`
	DiskGB               int                 `json:"disk_gb"`
	RuntimeStatus        string              `json:"runtime_status"`
	Metrics              *nodefabric.Metrics `json:"metrics,omitempty"`
	MetricsPartial       bool                `json:"metrics_partial,omitempty"`
}

func (c *signedClient) heartbeat(ctx context.Context, in heartbeatBody) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	var out nodefabric.HeartbeatOutput
	return c.do(ctx, http.MethodPost, pathHeartbeat, body, &out, "")
}
