// [INPUT]: 依赖 platform 的 crypto/db/httpx/realtime/geoip
// [OUTPUT]: 对外提供 Service、NewService、SetGeoIP、SetPreviousConfigSigner，身份 Identity / LookupIdentity、签名请求 CanonicalPayload / CanonicalPayloadV2 / DecodeNodeRequestNonce / ClaimSignedRequest；包内提供 canonicalJSON 等共用助手
// [POS]: domain/nodefabric 的主服务：构造与注入、节点身份校验（NODE-014，吊销即失效）与签名请求防重放；用例按专题分在同包文件：bootstrap.go（令牌与旧版接入）、heartbeat.go（心跳与探针）、config_delivery.go / config_publish.go（旧版配置签发、回报与发布）、enrollment.go、uniproxy.go、node_admin.go 等
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package nodefabric 实现节点接入与配置下发（PRD 第 8–9 章）。
//
// 认证方案的取舍：AGT-001 要求 Agent 主动建立 mTLS 长连接。首版改用
// 「Agent 主动轮询 + 每请求 Ed25519 签名」，理由是它同样满足那条要求的实质 ——
// 节点不开放任何监听端口、连接一律由 Agent 发起 —— 却不需要先立起一套证书
// 签发与吊销基础设施。node_identities 表已按证书模型建好（serial 单调、可吊销），
// 将来换成 mTLS 时身份模型不用动，只换传输层。
package nodefabric

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/geoip"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

type Service struct {
	pool           *db.Pool
	signer         *crypto.Signer // 给节点配置签名（AGT-007）
	previousSigner *crypto.Signer
	// stream 是节点事件流的注册表，可为 nil（没启用推送时）。
	stream       *StreamHub
	realtime     *realtime.Hub
	watchMu      sync.Mutex
	nodeWatchers map[string]context.CancelFunc
	// geoIP 把节点上报的公网 IP 翻成地区（省/州），Agent 首次接入时
	// 自动填 servers.region，管理员不必手抄。可以为 nil：数据文件
	// 缺失时地区留空，接入本身照常。
	geoIP *geoip.Resolver
}

func NewService(pool *db.Pool, signer *crypto.Signer) *Service {
	return &Service{pool: pool, signer: signer, nodeWatchers: make(map[string]context.CancelFunc)}
}

// SetGeoIP 注入 IP 归属地库（可空，缺库时自动识别地区静默降级为空）。
func (s *Service) SetGeoIP(r *geoip.Resolver) {
	s.geoIP = r
}

// regionForIP 用 ip2region 把公网 IP 翻成地区文本（如「日本 东京都 东京 亚马逊」）。
// 只取国家+省份+城市三段，运营商那截对线路名没意义。空 IP 或没配库返回空串。
func (s *Service) regionForIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" || s.geoIP == nil {
		return ""
	}
	loc := s.geoIP.Lookup(ip)
	segs := make([]string, 0, 3)
	for _, v := range []string{loc.Country, loc.Region, loc.City} {
		if v != "" {
			segs = append(segs, v)
		}
	}
	return strings.Join(segs, " ")
}

// SetPreviousConfigSigner enables a bounded old-to-new trust transition. The
// previous key only certifies the current public key and never signs configs.
func (s *Service) SetPreviousConfigSigner(previous *crypto.Signer) error {
	if previous != nil && previous.KeyID() == s.signer.KeyID() {
		return errors.New("previous config signer must differ from current signer")
	}
	s.previousSigner = previous
	return nil
}

//------------------------------------------------------------------------------
// 身份校验（NODE-014：吊销后旧身份立即失效）
//------------------------------------------------------------------------------

type Identity struct {
	NodeID    string
	Serial    int
	PublicKey ed25519.PublicKey
	Status    string
}

// LookupIdentity 取节点当前有效身份。只返回 active 的那一份 ——
// 吊销、过期、被更高 serial 取代的身份一律查不到，旧 Agent 立刻失去访问。
func (s *Service) LookupIdentity(ctx context.Context, tenantID, nodeID string) (*Identity, error) {
	var id Identity
	var pub []byte
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT i.node_id, i.serial, i.public_key, i.status
			  FROM node_identities i
			  JOIN nodes n ON n.tenant_id=i.tenant_id AND n.id=i.node_id
			 WHERE i.tenant_id=$1 AND i.node_id=$2 AND i.status='active' AND i.expires_at > now()
			   AND n.status NOT IN ('destroyed','retired') AND n.serving_status<>'retired'`,
			tenantID, nodeID).Scan(&id.NodeID, &id.Serial, &pub, &id.Status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.New(httpx.CodeUnauthorized, "节点身份无效")
	}
	if err != nil {
		return nil, err
	}
	id.PublicKey = pub
	return &id, nil
}

// CanonicalPayload 构造待签名串。Agent 与服务端必须用完全一致的规则，
// 任何一方多一个分隔符都会导致全量验签失败，所以这个函数是唯一来源。
func CanonicalPayload(method, path, nodeID, ts string, bodyHash []byte) []byte {
	return []byte(method + "\n" + path + "\n" + nodeID + "\n" + ts + "\n" +
		base64.StdEncoding.EncodeToString(bodyHash))
}

const nodeRequestSignatureDomainV2 = "PANDORA-NODE-REQUEST-V2"

// SignedRequestAcceptanceWindow is shared by timestamp validation and nonce
// retention. A nonce must remain claimed until its signature can no longer be
// accepted, including requests whose clocks are ahead of the server.
const SignedRequestAcceptanceWindow = 5 * time.Minute

// DecodeNodeRequestNonce accepts only the canonical raw URL-base64 encoding
// of a 128-bit nonce.
func DecodeNodeRequestNonce(encoded string) ([]byte, error) {
	if len(encoded) != 22 {
		return nil, fmt.Errorf("node request nonce must be 22 characters")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 16 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return nil, fmt.Errorf("invalid node request nonce")
	}
	return raw, nil
}

// CanonicalPayloadV2 binds the nonce and separates this signature protocol
// from both legacy V1 and unrelated Ed25519 uses.
func CanonicalPayloadV2(method, path, nodeID, ts, nonce string, bodyHash []byte) []byte {
	return []byte(nodeRequestSignatureDomainV2 + "\n" + method + "\n" + path + "\n" + nodeID + "\n" + ts + "\n" + nonce + "\n" +
		base64.StdEncoding.EncodeToString(bodyHash))
}

// ClaimSignedRequest atomically consumes a signed request nonce. Handler
// failures deliberately do not release it; a retry must use a fresh nonce.
func (s *Service) ClaimSignedRequest(ctx context.Context, tenantID, nodeID string, nonce, fingerprint []byte, requestTS time.Time) error {
	if len(nonce) != 16 || len(fingerprint) != sha256.Size {
		return httpx.New(httpx.CodeUnauthorized, "节点身份校验失败")
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var claimed int
		err := tx.QueryRow(ctx, `
			INSERT INTO node_request_nonces
				(tenant_id, node_id, nonce, request_fingerprint, request_ts, expires_at)
			VALUES ($1,$2,$3,$4,$5::timestamptz,
				GREATEST($5::timestamptz + INTERVAL '5 minutes', now() + INTERVAL '11 minutes'))
			ON CONFLICT (tenant_id, node_id, nonce) DO NOTHING
			RETURNING 1`, tenantID, nodeID, nonce, fingerprint, requestTS).Scan(&claimed)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeUnauthorized, "节点身份校验失败")
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			WITH expired AS (
				SELECT tenant_id, node_id, nonce
				  FROM node_request_nonces
				 WHERE tenant_id = $1 AND expires_at < now()
				 ORDER BY expires_at
				 LIMIT 32
			)
			DELETE FROM node_request_nonces n
			 USING expired e
			 WHERE n.tenant_id=e.tenant_id AND n.node_id=e.node_id AND n.nonce=e.nonce`, tenantID)
		return err
	})
	if err == nil {
		return nil
	}
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return httpx.New(httpx.CodeUnavailable, "节点认证服务暂不可用").WithInternal(err)
}

//------------------------------------------------------------------------------

// canonicalJSON 把任意 JSON 字节规范化为确定的字节序列。
//
// 为什么必须有这一步：payload 列是 jsonb，PostgreSQL 会按自己的规则
// 重排键、去掉空格后存储，读回来的字节与写进去的几乎必然不同。
// 若发布时对原始字节签名、下发时对读回的字节验签，签名永远对不上。
// Go 的 json.Marshal 对 map 按键名排序输出，因此两侧各自规范化后必定一致。
func canonicalJSON(raw []byte) ([]byte, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, fmt.Errorf("配置不是无重复字段的 JSON 对象: %w", err)
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("配置不是 JSON 对象: %w", err)
	}
	if m == nil {
		return nil, errors.New("配置必须是 JSON 对象")
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("配置包含多余 JSON 数据: %w", err)
	}
	return json.Marshal(m)
}

// bytesEqual 用于比较哈希。长度不同直接判否，避免越界。
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
func nullInt(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}
