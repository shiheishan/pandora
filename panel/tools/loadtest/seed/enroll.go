// [INPUT]: 依赖 domain/nodefabric 的接入与签名请求规范串（CanonicalEnrollmentBeginV1 / CanonicalEnrollmentRequestV1 / CanonicalPayloadV2），
//          依赖 platform/crypto 的 NewSigner / NewToken / HashToken；对端是 aegis-node 的 /v1/nodes/enrollments 与签名节点接口
// [OUTPUT]: 包内提供 nodeIdentity（newNodeIdentity）、nodeClient（enroll / signedGet / uniProxyGet）、enrollResult
// [POS]: tools/loadtest/seed 的节点侧：扮演刚装好的 pdnd，本地生成 Ed25519 密钥与运行令牌，用接入令牌走两段式接入 begin → commit；
//        签名规范串一律引用 nodefabric 的唯一实现，绝不在这里另抄一份；verify.go 用 signedGet 与 uniProxyGet 核对节点真能被服务

package seed

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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/crypto"
)

// nodeIdentity 是一个模拟节点在本地生成、只交给面板公钥与令牌哈希的那套身份材料。
type nodeIdentity struct {
	signer *crypto.Signer
	// PrivateKey 是 64 字节 Ed25519 私钥（种子 + 公钥），manifest 存它的标准 base64
	PrivateKey ed25519.PrivateKey
	// RuntimeToken 与 pdnd 同形：32 字节随机数的 base64url 无填充，面板只收它的 sha256
	RuntimeToken string
}

func newNodeIdentity() (*nodeIdentity, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	signer, err := crypto.NewSigner(seed)
	if err != nil {
		return nil, err
	}
	token, err := crypto.NewToken(32)
	if err != nil {
		return nil, err
	}
	return &nodeIdentity{signer: signer, PrivateKey: ed25519.NewKeyFromSeed(seed), RuntimeToken: token}, nil
}

func (id *nodeIdentity) publicKeyB64() string {
	return base64.StdEncoding.EncodeToString(id.signer.PublicKey())
}

func (id *nodeIdentity) sign(payload []byte) string {
	return base64.StdEncoding.EncodeToString(id.signer.Sign(payload))
}

func sha256Std(text string) string { return base64.StdEncoding.EncodeToString(crypto.HashToken(text)) }

// enrollResult 是接入提交之后节点端记住的东西。
type enrollResult struct {
	NodeID          string
	Serial          int
	ConfigKeyID     string
	ConfigPublicKey string
}

type nodeClient struct {
	base string
	http *http.Client
	// 接入提交上报的发布物证据（非生产且没钉产物时只校验格式）
	agentVersion string
	binarySHA256 string
	now          func() time.Time
}

func newNodeClient(base, agentVersion, binarySHA256 string) *nodeClient {
	return &nodeClient{base: base, agentVersion: agentVersion, binarySHA256: binarySHA256,
		http: &http.Client{Timeout: 60 * time.Second}, now: time.Now}
}

// enroll 走两段式接入：begin 用接入令牌占位并登记候选公钥，commit 用候选私钥签名后激活身份与运行令牌。
func (c *nodeClient) enroll(ctx context.Context, id *nodeIdentity, nodeName, bootstrapToken, wantNodeID, hostname string) (*enrollResult, error) {
	const beginPath = "/v1/nodes/enrollments"
	requestID := uuid.NewString()
	beginBody, err := json.Marshal(jsonObject{
		"token": bootstrapToken, "node_name": nodeName, "request_id": requestID,
		"public_key": id.publicKeyB64(), "runtime_token_sha256": sha256Std(id.RuntimeToken),
		"agent_version": c.agentVersion, "hostname": hostname,
	})
	if err != nil {
		return nil, err
	}
	beginHash := sha256.Sum256(beginBody)
	begin, err := c.do(ctx, http.MethodPost, beginPath, beginBody, map[string]string{
		"X-Enrollment-Signature": id.sign(nodefabric.CanonicalEnrollmentBeginV1(beginPath, requestID, beginHash[:])),
	}, http.StatusCreated)
	if err != nil {
		return nil, fmt.Errorf("enrollment begin %s: %w", nodeName, err)
	}
	var out enrollResult
	enrollmentID, err := str(begin, "enrollment_id")
	if err != nil {
		return nil, err
	}
	if out.NodeID, err = str(begin, "node_id"); err != nil {
		return nil, err
	}
	if out.NodeID != wantNodeID {
		return nil, fmt.Errorf("enrollment of %s landed on node %s, want %s", nodeName, out.NodeID, wantNodeID)
	}
	serial, err := num(begin, "serial")
	if err != nil {
		return nil, err
	}
	out.Serial = int(serial)
	if out.ConfigKeyID, err = str(begin, "config_key_id"); err != nil {
		return nil, err
	}
	if out.ConfigPublicKey, err = str(begin, "config_public_key"); err != nil {
		return nil, err
	}

	commitPath := "/v1/nodes/enrollments/" + enrollmentID + "/commit"
	commitBody, err := json.Marshal(jsonObject{
		"agent_version": c.agentVersion, "architecture": "amd64",
		"binary_sha256": c.binarySHA256, "config_sha256": placeholderDigest('2'),
		"unit_sha256": placeholderDigest('3'), "preflight_sha256": placeholderDigest('4'),
	})
	if err != nil {
		return nil, err
	}
	ts, nonce, err := c.stamp()
	if err != nil {
		return nil, err
	}
	commitHash := sha256.Sum256(commitBody)
	commit, err := c.do(ctx, http.MethodPost, commitPath, commitBody, map[string]string{
		"X-Node-Id": out.NodeID, "X-Node-Serial": strconv.Itoa(out.Serial), "X-Node-Ts": ts, "X-Node-Nonce": nonce,
		"X-Node-Sig": id.sign(nodefabric.CanonicalEnrollmentRequestV1(http.MethodPost, commitPath, enrollmentID,
			out.NodeID, out.Serial, ts, nonce, commitHash[:])),
	}, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("enrollment commit %s: %w", nodeName, err)
	}
	if state, _ := str(commit, "state"); state != "committed" {
		return nil, fmt.Errorf("enrollment commit %s: state %q, want committed", nodeName, state)
	}
	return &out, nil
}

// signedGet 以节点身份发一个 V2 签名的 GET（心跳、配置等 /v1/nodes/* 同一套头）。
func (c *nodeClient) signedGet(ctx context.Context, id *nodeIdentity, nodeID, path string) (jsonObject, error) {
	ts, nonce, err := c.stamp()
	if err != nil {
		return nil, err
	}
	empty := sha256.Sum256(nil)
	return c.do(ctx, http.MethodGet, path, nil, map[string]string{
		"X-Node-Id": nodeID, "X-Node-Ts": ts, "X-Node-Nonce": nonce,
		"X-Node-Sig": id.sign(nodefabric.CanonicalPayloadV2(http.MethodGet, path, nodeID, ts, nonce, empty[:])),
	}, http.StatusOK)
}

// uniProxyGet 以运行令牌调 UniProxy 兼容接口（config / user）。
func (c *nodeClient) uniProxyGet(ctx context.Context, runtimeToken, nodeID, nodeType, what string) (jsonObject, error) {
	q := url.Values{"node_id": {nodeID}, "node_type": {nodeType}}
	return c.do(ctx, http.MethodGet, "/api/v1/server/UniProxy/"+what+"?"+q.Encode(), nil,
		map[string]string{"Authorization": "Bearer " + runtimeToken}, http.StatusOK)
}

// stamp 给出签名头要的秒级 UTC 时间戳与一次性 nonce（16 字节 base64url 无填充）。
func (c *nodeClient) stamp() (string, string, error) {
	nonce, err := crypto.NewToken(16)
	if err != nil {
		return "", "", err
	}
	return c.now().UTC().Format(time.RFC3339), nonce, nil
}

// edgeRetries 是节点侧请求撞边缘限流时的重试次数。nginx 对 /v1/nodes/ 按来源 IP 限 240 次/分、突发 60，
// seed 在面板机上从一个地址并发接入两百个节点，必然撞上；被 nginx 挡下的请求没进应用，原样重放是安全的。
const edgeRetries = 12

func (c *nodeClient) do(ctx context.Context, method, pathAndQuery string, body []byte, headers map[string]string, want int) (jsonObject, error) {
	var (
		resp *http.Response
		raw  []byte
	)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+pathAndQuery, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("User-Agent", "pandora-loadtest-seed")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if resp, err = c.http.Do(req); err != nil {
			return nil, err
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if !edgeLimited(resp) || attempt >= edgeRetries {
			break
		}
		if err := sleepCtx(ctx, edgeBackoff(resp, attempt)); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != want {
		return nil, newAPIError(method, pathAndQuery, resp.StatusCode, raw)
	}
	out := jsonObject{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s %s: decode: %w", method, pathAndQuery, err)
	}
	return out, nil
}

func placeholderDigest(c byte) string { return string(bytes.Repeat([]byte{c}, 64)) }

// edgeLimited 判断响应是不是限流挡回来的：应用的 429，或 nginx limit_req 默认回的 503 HTML 页
// （应用自己的错误一律是 JSON，不会被误判成可重放）。
func edgeLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode == http.StatusServiceUnavailable &&
		strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html")
}

// edgeBackoff 优先听 Retry-After，否则 0.5s 起翻倍、封顶 8s；限流桶按秒回填，等太久只是拖慢接入。
func edgeBackoff(resp *http.Response, attempt int) time.Duration {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 500 * time.Millisecond << min(attempt, 4)
}
