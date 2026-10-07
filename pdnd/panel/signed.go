package panel

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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DefaultIdentityPath = "/var/lib/pandora-native/identity.json"

type Identity struct {
	Server          string `json:"server"`
	NodeID          string `json:"node_id"`
	Serial          int    `json:"serial"`
	PrivateKey      string `json:"private_key"`
	ConfigPublicKey string `json:"config_public_key"`
	ConfigKeyID     string `json:"config_key_id"`
	RuntimeToken    string `json:"runtime_token"`
}

// BootstrapOptions 是安装器 bootstrap 子命令的参数，交给 BeginEnrollment。
type BootstrapOptions struct {
	Server string
	Token  string
	Name   string
	Path   string
}

func validateSignedServer(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid panel server URL")
	}
	if u.Scheme == "https" {
		return base, nil
	}
	host := u.Hostname()
	if u.Scheme == "http" && (strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return base, nil
	}
	return "", fmt.Errorf("panel server URL must use HTTPS (HTTP is allowed only on loopback)")
}

// CanonicalSignedServer exposes the same strict HTTPS origin normalization used
// by signed requests so CLI identity checks cannot compare ambiguous strings.
func CanonicalSignedServer(raw string) (string, error) { return validateSignedServer(raw) }

func rejectCredentialRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func SaveIdentity(path string, identity *Identity) error {
	if identity == nil || identity.NodeID == "" || identity.PrivateKey == "" {
		return fmt.Errorf("invalid identity")
	}
	body, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, body, 0600)
}

func LoadIdentity(path string) (*Identity, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var identity Identity
	if err := json.Unmarshal(body, &identity); err != nil {
		return nil, err
	}
	priv, err := base64.StdEncoding.DecodeString(identity.PrivateKey)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid identity private key")
	}
	return &identity, nil
}

type signedClient struct {
	identity     *Identity
	identityPath string
	private      ed25519.PrivateKey
	http         *http.Client
	// keyCheckedAt 是上一次成功问过面板「配置签名密钥换了没有」的时间。签名密钥
	// 极少轮换，不必每轮拉配置都问一次（见 refreshConfigSigningKeyIfDue）。
	keyCheckedAt time.Time
	keyCheckWait time.Duration
	// onKeyCheckError 收例行换钥检查的失败。检查失败不挡拉配置（见 ConfigSince），
	// 但要让调用方记日志。
	onKeyCheckError func(error)
}

// OnKeyCheckError 设置例行换钥检查失败时的回调（节点端用来记日志）。
func (c *SignedClient) OnKeyCheckError(fn func(error)) { c.onKeyCheckError = fn }

type SignedClient = signedClient

func (c *SignedClient) ConfigSigningKeyID() string {
	return c.identity.ConfigKeyID
}

type SignedConfig struct {
	ConfigContract       string          `json:"config_contract"`
	TenantID             string          `json:"tenant_id"`
	NodeID               string          `json:"node_id"`
	ReleaseID            string          `json:"release_id"`
	Generation           uint64          `json:"generation"`
	ContentSHA256        string          `json:"content_sha256"`
	SourceManifest       json.RawMessage `json:"source_manifest"`
	SourceManifestSHA256 string          `json:"source_manifest_sha256"`
	IssuedAt             time.Time       `json:"issued_at"`
	Version              int             `json:"version"`
	Payload              json.RawMessage `json:"payload"`
	Hash                 string          `json:"hash"`
	Signature            string          `json:"signature"`
	KeyID                string          `json:"key_id"`
	ExpiresAt            time.Time       `json:"expires_at"`
	Sources              []string        `json:"sources"`
}

type HeartbeatInput struct {
	AgentVersion         string `json:"agent_version"`
	RuntimeVersion       string `json:"runtime_version"`
	ConfigSigningKeyID   string `json:"config_signing_key_id"`
	ConfigVersion        int    `json:"applied_config_version"`
	ConfigHash           string `json:"applied_config_hash"`
	AppliedReleaseID     string `json:"applied_effective_release_id,omitempty"`
	AppliedGeneration    uint64 `json:"applied_effective_generation,omitempty"`
	AppliedContentSHA256 string `json:"applied_effective_content_sha256,omitempty"`
	CPUCores             int    `json:"cpu_cores"`
	MemoryMB             int    `json:"memory_mb"`
	DiskGB               int    `json:"disk_gb"`
	RuntimeStatus        string `json:"runtime_status"`
	// RuntimeReason 是 RuntimeStatus 不是 running 时的机器可读原因（纯 ASCII），
	// 走请求头 RuntimeReasonHeader 而不进 JSON：面板心跳按 DisallowUnknownFields
	// 解码，多一个字段整条心跳就 400。面板侧接住之前它只是被忽略。
	RuntimeReason string `json:"-"`
	// Metrics 为空时面板不写 node_metrics；由 AttachHostMetrics 填
	Metrics        *HeartbeatMetrics `json:"metrics,omitempty"`
	MetricsPartial bool              `json:"metrics_partial,omitempty"`
}

type HeartbeatOutput struct {
	NodeStatus           string `json:"node_status"`
	DesiredConfigVersion int    `json:"desired_config_version"`
	DesiredReleaseID     string `json:"desired_effective_release_id,omitempty"`
	DesiredGeneration    uint64 `json:"desired_effective_generation,omitempty"`
	IntervalSeconds      int    `json:"interval_seconds"`
}

// RuntimeReasonHeader 带心跳的 degraded 原因，见 HeartbeatInput.RuntimeReason。
// 不进签名原像：它只是给人看的诊断信息，状态本身（runtime_status）在签名正文里。
const RuntimeReasonHeader = "X-Node-Runtime-Reason"

func (c *SignedClient) Heartbeat(ctx context.Context, in HeartbeatInput) (*HeartbeatOutput, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var headers map[string]string
	if in.RuntimeReason != "" {
		headers = map[string]string{RuntimeReasonHeader: in.RuntimeReason}
	}
	_, raw, err := c.do(ctx, http.MethodPost, "/v1/nodes/heartbeat", body, headers)
	if err != nil {
		return nil, err
	}
	var out HeartbeatOutput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// Config 拉一份完整的生效配置（不带已应用版本，面板总是回全量）。
func (c *SignedClient) Config(ctx context.Context) (*SignedConfig, error) {
	cfg, _, err := c.ConfigSince(ctx, nil)
	return cfg, err
}

// AppliedRelease 是节点手上已应用的生效发布版本。
type AppliedRelease struct {
	ReleaseID  string
	Generation uint64
}

// AppliedEffectiveReleaseHeader 与面板 nodefabric.AppliedEffectiveReleaseHeader 同名同格式。
const AppliedEffectiveReleaseHeader = "X-Applied-Effective-Release"

// ConfigSince 拉生效配置。applied 非空时把它报给面板：仍是当前版的话面板回 204，
// 这里返回 unchanged=true、cfg 为 nil——没有新东西要验、要装。
//
// 兼容：老面板不认这个头，照旧回 200 全量，调用方按「已应用」处理即可；新面板
// 只在收到这个头时才可能回 204，老节点不带它，不受影响。
//
// 例行换钥检查失败不挡这一次拉取：拿到的配置照样用钉住的公钥验签，验不过时
// 调用方会强制再查一次（fail-closed 不变）。原先检查一失败整轮配置同步就返回
// 错误，节点端连带不再拉用户名单，名单因此冻结。
func (c *SignedClient) ConfigSince(ctx context.Context, applied *AppliedRelease) (cfg *SignedConfig, unchanged bool, err error) {
	if err := c.refreshConfigSigningKeyIfDue(ctx); err != nil && c.onKeyCheckError != nil {
		c.onKeyCheckError(err)
	}
	var headers map[string]string
	if applied != nil && applied.ReleaseID != "" && applied.Generation > 0 {
		headers = map[string]string{AppliedEffectiveReleaseHeader: applied.ReleaseID + "/" +
			strconv.FormatUint(applied.Generation, 10)}
	}
	status, raw, err := c.do(ctx, http.MethodGet, "/v1/nodes/effective-config", nil, headers)
	if err != nil {
		return nil, false, err
	}
	if status == http.StatusNoContent {
		if headers == nil {
			return nil, false, fmt.Errorf("panel returned no effective config")
		}
		return nil, true, nil
	}
	var out SignedConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, err
	}
	return &out, false, nil
}

// configKeyCheckInterval 是主动问面板「签名密钥换了没有」的间隔。密钥轮换时面板
// 签出的过渡证明有效 15 分钟（configKeyTransitionWindow），10 分钟问一次足够在窗口
// 内拿到；而配置验签失败时调用方会立刻强制再问一次，轮换不必等这个间隔。
const configKeyCheckInterval = 10 * time.Minute

// refreshConfigSigningKeyIfDue 只在到点时才问面板换钥。原先每轮拉配置前都问一次，
// 200 个节点 15 秒一轮，这个几乎总回 204 的请求占了节点请求量的六分之一。
func (c *SignedClient) refreshConfigSigningKeyIfDue(ctx context.Context) error {
	if !c.keyCheckedAt.IsZero() && time.Since(c.keyCheckedAt) < c.keyCheckWait {
		return nil
	}
	return c.RefreshConfigSigningKey(ctx)
}

// markConfigKeyChecked 记下一次成功的换钥检查，下次检查的间隔带 ±10% 抖动。
func (c *SignedClient) markConfigKeyChecked() {
	c.keyCheckedAt = time.Now()
	c.keyCheckWait = Jitter(configKeyCheckInterval)
}

func (c *SignedClient) ReportConfig(ctx context.Context, version int, phase, detail string) error {
	body, err := json.Marshal(map[string]any{"version": version, "phase": phase, "detail": detail})
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost, "/v1/nodes/config/report", body, nil)
}

func (c *SignedClient) ReportEffectiveConfig(ctx context.Context, cfg *SignedConfig, phase, detail string) error {
	if cfg == nil {
		return fmt.Errorf("effective config is required")
	}
	releaseID, err := uuid.Parse(cfg.ReleaseID)
	if err != nil || releaseID == uuid.Nil || releaseID.String() != cfg.ReleaseID {
		return fmt.Errorf("effective release id must be a non-zero canonical UUID")
	}
	reportID := uuid.NewSHA1(releaseID, []byte(phase)).String()
	body, err := json.Marshal(map[string]any{
		"report_id": reportID, "release_id": cfg.ReleaseID,
		"generation": cfg.Generation, "content_sha256": cfg.ContentSHA256,
		"phase": phase, "detail": detail,
	})
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost, "/v1/nodes/config/report", body, nil)
}

func (c *SignedClient) VerifyConfig(cfg *SignedConfig) error {
	return c.verifyConfig(cfg, true)
}

// VerifyCachedConfig 校验从落盘缓存读回的签名配置：签名、内容哈希、节点身份、
// 钉住的公钥全部照验，只不查投递窗口（issued_at / expires_at）。
//
// 投递窗口防的是网络上重放旧签名；缓存是本节点曾经验过、装过的那一份，落在
// 只有服务账号可写的状态目录里，启动时面板不可达才会用到，面板一回来就被
// 当前版本替换。签名或身份对不上（篡改、换钥、换了节点）一律拒绝，fail-closed。
func (c *SignedClient) VerifyCachedConfig(cfg *SignedConfig) error {
	return c.verifyConfig(cfg, false)
}

func (c *SignedClient) verifyConfig(cfg *SignedConfig, checkWindow bool) error {
	if cfg == nil || len(cfg.Payload) == 0 {
		return fmt.Errorf("empty signed config")
	}
	if cfg.ConfigContract != "" {
		return c.verifyEffectiveConfig(cfg, checkWindow)
	}
	if c.identity.ConfigKeyID == "" || cfg.KeyID != c.identity.ConfigKeyID {
		return fmt.Errorf("config key id mismatch")
	}
	pubRaw, err := base64.StdEncoding.DecodeString(c.identity.ConfigPublicKey)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid config public key")
	}
	sum := sha256.Sum256(cfg.Payload)
	if base64.StdEncoding.EncodeToString(sum[:]) != cfg.Hash {
		return fmt.Errorf("config hash mismatch")
	}
	sig, err := base64.StdEncoding.DecodeString(cfg.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pubRaw), append(sum[:], []byte(cfg.ExpiresAt.UTC().Format(time.RFC3339))...), sig) {
		return fmt.Errorf("config signature invalid")
	}
	if checkWindow && time.Now().After(cfg.ExpiresAt) {
		return fmt.Errorf("config expired")
	}
	return nil
}

func NewSignedClient(identity *Identity) (*SignedClient, error) {
	return NewSignedClientAt(identity, "")
}

func NewSignedClientAt(identity *Identity, identityPath string) (*SignedClient, error) {
	if identity == nil {
		return nil, fmt.Errorf("identity is required")
	}
	server, err := validateSignedServer(identity.Server)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(identity.PrivateKey)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid identity private key")
	}
	identity.Server = server
	return &SignedClient{identity: identity, identityPath: identityPath, private: ed25519.PrivateKey(raw), http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: rejectCredentialRedirect}}, nil
}

// StatusError 是面板对签名请求回了非 2xx：请求已送达、面板给了明确答复。
// 与传输错误（*url.Error）分开，调用方才能区分「面板拒收」与「没送到」——
// 前者重发同一份证据也只会再被拒，后者下一轮值得再发。
type StatusError struct {
	// What 是兼容通道的动作描述（「拉取配置」之类），签名通道为空。
	What   string
	Method string
	Path   string
	Code   int
	Body   string
}

func (e *StatusError) Error() string {
	if e.What != "" {
		return fmt.Sprintf("%s 失败：HTTP %d %s", e.What, e.Code, e.Body)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Code, e.Body)
}

// IsRejection 判断 err 是不是面板的明确拒绝：4xx（408 / 429 除外）。
//
// 请求送到了、面板给了答复，重发同一份东西只会再被拒；与之相对的传输错误、
// 5xx、408、429 都是「暂时没送到」。节点端据此决定回执要不要补报、启动时
// 能不能用落盘缓存（面板明确拒绝这个节点时不该拿缓存绕过去）。
func IsRejection(err error) bool {
	var status *StatusError
	if !errors.As(err, &status) {
		return false
	}
	return status.Code >= 400 && status.Code < 500 &&
		status.Code != http.StatusRequestTimeout && status.Code != http.StatusTooManyRequests
}

func (c *SignedClient) Do(ctx context.Context, method, path string, body []byte, out any) error {
	_, raw, err := c.do(ctx, method, path, body, nil)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// do 发一个签名请求，返回 2xx 的状态码与正文；非 2xx 返回 *StatusError。headers 是
// 额外的请求头，不进签名原像（原像只认方法、路径、节点、时间、nonce 与正文）。
func (c *SignedClient) do(ctx context.Context, method, path string, body []byte, headers map[string]string) (int, []byte, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		return 0, nil, fmt.Errorf("generate request nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	sum := sha256.Sum256(body)
	payload := []byte("PANDORA-NODE-REQUEST-V2\n" + method + "\n" + path + "\n" + c.identity.NodeID + "\n" + ts + "\n" + nonce + "\n" + base64.StdEncoding.EncodeToString(sum[:]))
	sig := ed25519.Sign(c.private, payload)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.identity.Server, "/")+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", c.identity.NodeID)
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Nonce", nonce)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(sig))
	if path == "/v1/nodes/config-signing-key" {
		req.Header.Set("X-Config-Key-Id", c.identity.ConfigKeyID)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, nil, &StatusError{Method: method, Path: path, Code: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	return resp.StatusCode, raw, nil
}

func hostname() string { h, _ := os.Hostname(); return h }
