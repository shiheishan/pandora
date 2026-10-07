package bindingcontract

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	// ManifestStateBound 是正常清单。
	ManifestStateBound = "bound"
	// ManifestStateUnbound 是签名的注销墓碑：pdnd 验签通过后停掉该绑定名下全部入站并清理。
	ManifestStateUnbound = "unbound"

	// MaxManifestNodes 限制一份清单的节点数，防止异常清单打爆节点内存。
	MaxManifestNodes = 4096
)

// UnboundReasons 是墓碑 unbound_reason 的取值。
var UnboundReasons = []string{"server_deleted", "server_unbound", "identity_revoked", "machine_unbound"}

// ManifestIntervals 是面板下发的节拍（秒），每项都在 [5, 3600] 之内。
type ManifestIntervals struct {
	ManifestPullSeconds     int // 事件流断开时拉清单的间隔
	UsersPullSeconds        int // 事件流断开时拉名单的间隔
	ReportSeconds           int // 合并上报 S5 的间隔
	StreamOnlinePullSeconds int // 事件流在线时清单与名单的兜底拉取间隔
}

// ManifestNode 是清单里的一个节点（入站）。
type ManifestNode struct {
	NodeID              string
	Protocol            string // 小写协议名，如 vless、hysteria2
	Port                int
	L4                  string // tcp 或 udp，与 pdnd 端口登记表的键一致
	PoolID              string // 节点所在池；节点不在任何池时为空串（此时名单为空）
	EffectiveGeneration uint64 // 该节点当前生效发布的 generation
	ContentSHA256       string // 该节点当前生效发布的 content_sha256
}

// ManifestFields 是服务器清单签名覆盖的全部字段。节点顺序有意义（面板按节点创建时间排，
// pdnd 冷启动按此顺序串行首次 bind），原像按给定顺序逐行写出，不排序。
type ManifestFields struct {
	TenantID           string
	ServerID           string
	Serial             int64 // 服务器身份序号：换身份后旧身份的清单不再被接受
	ManifestGeneration uint64
	State              string // bound | unbound
	UnboundReason      string // 只在 unbound 时非空，取值见 UnboundReasons
	Features           []string
	MinAgentVersion    string // vX.Y.Z，或空串表示不限
	TLSPinNext         string // 预告的下一个网关 SPKI 钉住值，或空串
	Intervals          ManifestIntervals
	Nodes              []ManifestNode
	RequestNonce       string // 本次清单请求的 X-Server-Nonce
	KeyID              string // 面板配置签名公钥的 key id
	IssuedAt           string
	ExpiresAt          string
}

// ManifestPreimage 返回面板配置私钥签名、pdnd 独立重建的清单原像。
func ManifestPreimage(in ManifestFields) ([]byte, error) {
	if err := checkUUID("tenant_id", in.TenantID); err != nil {
		return nil, err
	}
	if err := checkUUID("server_id", in.ServerID); err != nil {
		return nil, err
	}
	if err := checkSerial(in.Serial); err != nil {
		return nil, err
	}
	if err := checkGeneration("manifest_generation", in.ManifestGeneration); err != nil {
		return nil, err
	}
	switch in.State {
	case ManifestStateBound:
		if in.UnboundReason != "" {
			return nil, errors.New("unbound_reason must be empty for a bound manifest")
		}
	case ManifestStateUnbound:
		if !slices.Contains(UnboundReasons, in.UnboundReason) {
			return nil, errors.New("unbound manifest needs a known unbound_reason")
		}
		if len(in.Nodes) != 0 {
			return nil, errors.New("unbound manifest must not list nodes")
		}
	default:
		return nil, errors.New("state must be bound or unbound")
	}
	features, err := joinFeatures(in.Features)
	if err != nil {
		return nil, err
	}
	if in.MinAgentVersion != "" {
		if _, err := ParseAgentVersion(in.MinAgentVersion); err != nil {
			return nil, fmt.Errorf("min_agent_version: %w", err)
		}
	}
	if in.TLSPinNext != "" {
		if _, err := ParseTLSPin(in.TLSPinNext); err != nil {
			return nil, fmt.Errorf("tls_pin_next: %w", err)
		}
	}
	for _, iv := range []struct {
		name  string
		value int
	}{
		{"manifest_pull_seconds", in.Intervals.ManifestPullSeconds},
		{"users_pull_seconds", in.Intervals.UsersPullSeconds},
		{"report_seconds", in.Intervals.ReportSeconds},
		{"stream_online_pull_seconds", in.Intervals.StreamOnlinePullSeconds},
	} {
		if iv.value < 5 || iv.value > 3600 {
			return nil, fmt.Errorf("%s must be within [5, 3600]", iv.name)
		}
	}
	if len(in.Nodes) > MaxManifestNodes {
		return nil, fmt.Errorf("manifest lists more than %d nodes", MaxManifestNodes)
	}
	if err := checkNonce("request_nonce", in.RequestNonce); err != nil {
		return nil, err
	}
	if err := checkKeyID("key_id", in.KeyID); err != nil {
		return nil, err
	}
	if err := canonicalWindow(in.IssuedAt, in.ExpiresAt); err != nil {
		return nil, err
	}

	var b strings.Builder
	b.Grow(640 + 220*len(in.Nodes))
	b.WriteString(ManifestContract)
	b.WriteByte('\n')
	line(&b, "tenant_id", in.TenantID)
	line(&b, "server_id", in.ServerID)
	line(&b, "serial", formatInt(in.Serial))
	line(&b, "manifest_generation", formatUint(in.ManifestGeneration))
	line(&b, "state", in.State)
	line(&b, "unbound_reason", in.UnboundReason)
	line(&b, "features", features)
	line(&b, "min_agent_version", in.MinAgentVersion)
	line(&b, "tls_pin_next", in.TLSPinNext)
	line(&b, "manifest_pull_seconds", strconv.Itoa(in.Intervals.ManifestPullSeconds))
	line(&b, "users_pull_seconds", strconv.Itoa(in.Intervals.UsersPullSeconds))
	line(&b, "report_seconds", strconv.Itoa(in.Intervals.ReportSeconds))
	line(&b, "stream_online_pull_seconds", strconv.Itoa(in.Intervals.StreamOnlinePullSeconds))
	line(&b, "request_nonce", in.RequestNonce)
	line(&b, "key_id", in.KeyID)
	line(&b, "issued_at", in.IssuedAt)
	line(&b, "expires_at", in.ExpiresAt)
	line(&b, "node_count", strconv.Itoa(len(in.Nodes)))
	seen := make(map[string]bool, len(in.Nodes))
	for i, n := range in.Nodes {
		if err := checkManifestNode(n); err != nil {
			return nil, fmt.Errorf("nodes[%d]: %w", i, err)
		}
		if seen[n.NodeID] {
			return nil, fmt.Errorf("nodes[%d]: duplicate node_id", i)
		}
		seen[n.NodeID] = true
		// 一行一个节点：值的字符集都不含空格与 =（content_sha256 的填充 = 只出现在值尾），
		// 按固定键序写出即单射。
		b.WriteString("node node_id=")
		b.WriteString(n.NodeID)
		b.WriteString(" protocol=")
		b.WriteString(n.Protocol)
		b.WriteString(" port=")
		b.WriteString(strconv.Itoa(n.Port))
		b.WriteString(" l4=")
		b.WriteString(n.L4)
		b.WriteString(" pool_id=")
		b.WriteString(n.PoolID)
		b.WriteString(" effective_generation=")
		b.WriteString(formatUint(n.EffectiveGeneration))
		b.WriteString(" content_sha256=")
		b.WriteString(n.ContentSHA256)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

func checkManifestNode(n ManifestNode) error {
	if err := checkUUID("node_id", n.NodeID); err != nil {
		return err
	}
	if !isToken(n.Protocol, 32, false) {
		return errors.New("protocol must match [a-z0-9][a-z0-9-]{0,31}")
	}
	if n.Port < 1 || n.Port > 65535 {
		return errors.New("port must be within [1, 65535]")
	}
	if n.L4 != "tcp" && n.L4 != "udp" {
		return errors.New("l4 must be tcp or udp")
	}
	if n.PoolID != "" {
		if err := checkUUID("pool_id", n.PoolID); err != nil {
			return err
		}
	}
	if err := checkGeneration("effective_generation", n.EffectiveGeneration); err != nil {
		return err
	}
	return checkSHA256B64("content_sha256", n.ContentSHA256)
}

// joinFeatures 要求特性名严格升序、不重复，每个匹配 [a-z0-9][a-z0-9.-]{0,47}，用逗号连接。
func joinFeatures(features []string) (string, error) {
	for i, f := range features {
		if !isToken(f, 48, true) {
			return "", fmt.Errorf("features[%d] must match [a-z0-9][a-z0-9.-]{0,47}", i)
		}
		if i > 0 && f <= features[i-1] {
			return "", errors.New("features must be strictly ascending and unique")
		}
	}
	return strings.Join(features, ","), nil
}

// isToken 判断小写标识符：首字符 [a-z0-9]，其余 [a-z0-9-]（allowDot 时再加 .）。
func isToken(s string, max int, allowDot bool) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if i > 0 {
			ok = ok || c == '-' || allowDot && c == '.'
		}
		if !ok {
			return false
		}
	}
	return true
}
