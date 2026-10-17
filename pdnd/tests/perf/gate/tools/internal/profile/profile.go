// Package profile 提供节点端性能闸门的协议清单、虚构用户与配置文档。
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	VLESSVision = "vless_vision"
	VLESSGRPC   = "vless_grpc"
	TrojanTCP   = "trojan_tcp"
	TrojanWS    = "trojan_ws"
	SS          = "ss"
	VMessWS     = "vmess_ws"
	Hy2         = "hy2"
	TUIC        = "tuic"
	AnyTLS      = "anytls"

	GRPCService    = "ngrpc"
	TrojanWSPath   = "/ng-trojan"
	VMessWSPath    = "/ng-vmess"
	SSMethod       = "aes-128-gcm"
	TUICCongestion = "cubic"

	ServerName        = "node.example.test"
	RealityServerName = "www.example.test"
)

// All 是协议全集的固定顺序。
var All = []string{VLESSVision, VLESSGRPC, TrojanTCP, TrojanWS, SS, VMessWS, Hy2, TUIC, AnyTLS}

// IsQUIC 判断是否为 QUIC 类协议（hy2、tuic）。
func IsQUIC(proto string) bool {
	return proto == Hy2 || proto == TUIC
}

// NodeType 返回协议对应的 node_type。
func NodeType(proto string) string {
	switch proto {
	case VLESSVision, VLESSGRPC:
		return "vless"
	case TrojanTCP, TrojanWS:
		return "trojan"
	case SS:
		return "shadowsocks"
	case VMessWS:
		return "vmess"
	case Hy2:
		return "hysteria2"
	case TUIC:
		return "tuic"
	case AnyTLS:
		return "anytls"
	default:
		return ""
	}
}

// ParseProtocols 解析 "all" 或逗号分隔的协议子集，按 All 顺序返回，去重；未知名报错。
func ParseProtocols(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("协议列表为空")
	}
	want := make(map[string]bool)
	if strings.EqualFold(s, "all") {
		for _, p := range All {
			want[p] = true
		}
	} else {
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if NodeType(part) == "" {
				return nil, fmt.Errorf("未知协议: %s", part)
			}
			want[part] = true
		}
		if len(want) == 0 {
			return nil, errors.New("协议列表为空")
		}
	}
	out := make([]string, 0, len(want))
	for _, p := range All {
		if want[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// UserUUID 按契约 §2 从 seed 与下标 i 派生用户 UUID（小写 8-4-4-4-12）。
func UserUUID(seed string, i int) string {
	return uuidFromHash("nodegate-user:" + seed + ":" + strconv.Itoa(i))
}

// NodeID 按契约 §2 从 seed 与协议 id 派生节点 UUID。
func NodeID(seed, proto string) string {
	return uuidFromHash("nodegate-node:" + seed + ":" + proto)
}

func uuidFromHash(prefix string) string {
	sum := sha256.Sum256([]byte(prefix))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Reality 是 REALITY 相关配置（写入 profile.json）。
type Reality struct {
	ServerName string `json:"server_name"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	ShortID    string `json:"short_id"`
	Dest       string `json:"dest"`
}

// Cert 是 TLS 证书文件路径与叶子摘要。
type Cert struct {
	CAFile     string `json:"ca_file"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	LeafSHA256 string `json:"leaf_sha256"`
}

// Profile 是 scalepanel init 写出、各工具读取的配置。
type Profile struct {
	Version      int      `json:"version"`
	Seed         string   `json:"seed"`
	Users        int      `json:"users"`
	DeltaSize    int      `json:"delta_size"`
	PortBase     int      `json:"port_base"`
	Protocols    []string `json:"protocols"`
	ServerName   string   `json:"server_name"`
	Reality      Reality  `json:"reality"`
	Cert         Cert     `json:"cert"`
	NodeCertPath string   `json:"node_cert_path"`
	NodeKeyPath  string   `json:"node_key_path"`
	Dir          string   `json:"-"` // profile.json 所在目录，Load 时填
}

// User 是名单中的一名用户。
type User struct {
	Index int
	UID   int64
	UUID  string
}

// InitOptions 是 Init 的输入。
type InitOptions struct {
	Dir, Seed                  string
	Users, DeltaSize, PortBase int
	Protocols                  []string
	RealityDest                string // target 诱饵站地址（host:port）
	NodeCertDir                string // pdnd 读证书的目录；空则用 Dir/certs 的绝对路径
}

// Load 读取 profile.json，校验并填充 Dir。
func Load(path string) (*Profile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	p.Dir = filepath.Dir(abs)
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Profile) validate() error {
	if p.Version != 1 {
		return fmt.Errorf("version 必须是 1，得到 %d", p.Version)
	}
	if p.Users <= 0 || p.DeltaSize <= 0 || p.Users <= p.DeltaSize {
		return fmt.Errorf("users 必须大于 delta_size 且都大于 0")
	}
	if p.PortBase < 1 || p.PortBase > 65526 {
		return fmt.Errorf("port_base 必须在 1..65526")
	}
	for _, proto := range p.Protocols {
		if NodeType(proto) == "" {
			return fmt.Errorf("非法协议名: %s", proto)
		}
	}
	return nil
}

func indexInAll(proto string) int {
	for i, p := range All {
		if p == proto {
			return i
		}
	}
	return -1
}

func (p *Profile) protocolEnabled(proto string) bool {
	for _, x := range p.Protocols {
		if x == proto {
			return true
		}
	}
	return false
}

// Port 返回协议入站端口（按全集序号 + port_base）。
func (p *Profile) Port(proto string) int {
	i := indexInAll(proto)
	if i < 0 {
		return 0
	}
	return p.PortBase + i
}

// StableUsers 返回稳定核人数 Users - DeltaSize。
func (p *Profile) StableUsers() int {
	return p.Users - p.DeltaSize
}

// RosterVersion 返回名单版本串。
func (p *Profile) RosterVersion(gen int) string {
	return fmt.Sprintf("ng-g%d", gen)
}

func blockRange(p *Profile, gen int) (start, end int) {
	K := p.DeltaSize
	N := p.Users
	start = N - K + gen*K
	end = N + gen*K
	return start, end
}

func userAt(p *Profile, i int) User {
	return User{Index: i, UID: int64(i + 1), UUID: UserUUID(p.Seed, i)}
}

// Roster 返回第 gen 代全名单：稳定核 [0,N-K) ∪ block(gen)，按下标升序。
func (p *Profile) Roster(gen int) []User {
	N := p.Users
	K := p.DeltaSize
	stableEnd := N - K
	bs, be := blockRange(p, gen)
	out := make([]User, 0, N)
	for i := 0; i < stableEnd; i++ {
		out = append(out, userAt(p, i))
	}
	for i := bs; i < be; i++ {
		out = append(out, userAt(p, i))
	}
	return out
}

// Delta 返回 gen-1 → gen 的增量；gen < 1 时两者皆空。
func (p *Profile) Delta(gen int) (upserted []User, removedUIDs []int64) {
	if gen < 1 {
		return nil, nil
	}
	bs, be := blockRange(p, gen)
	for i := bs; i < be; i++ {
		upserted = append(upserted, userAt(p, i))
	}
	pbs, pbe := blockRange(p, gen-1)
	for i := pbs; i < pbe; i++ {
		removedUIDs = append(removedUIDs, int64(i+1))
	}
	sort.Slice(removedUIDs, func(i, j int) bool { return removedUIDs[i] < removedUIDs[j] })
	return upserted, removedUIDs
}

func (p *Profile) realityDocFields() map[string]any {
	return map[string]any{
		"dest":         p.Reality.Dest,
		"server_names": []string{p.Reality.ServerName},
		"private_key":  p.Reality.PrivateKey,
		"public_key":   p.Reality.PublicKey,
		"short_ids":    []string{p.Reality.ShortID},
	}
}

// ConfigDoc 按 §4 生成节点配置文档字节与小写 hex 的 SHA-256。
func (p *Profile) ConfigDoc(proto string) ([]byte, string, error) {
	if NodeType(proto) == "" {
		return nil, "", fmt.Errorf("未知协议: %s", proto)
	}
	if !p.protocolEnabled(proto) {
		return nil, "", fmt.Errorf("协议不在 profile 中: %s", proto)
	}
	doc := make(map[string]any)
	switch proto {
	case VLESSVision:
		doc["network"] = "tcp"
		doc["security"] = "reality"
		doc["flow"] = "xtls-rprx-vision"
		for k, v := range p.realityDocFields() {
			doc[k] = v
		}
		doc["fingerprint"] = "chrome"
	case VLESSGRPC:
		doc["network"] = "grpc"
		doc["grpc_service_name"] = GRPCService
		doc["security"] = "reality"
		for k, v := range p.realityDocFields() {
			doc[k] = v
		}
		doc["fingerprint"] = "chrome"
	case TrojanTCP:
		doc["network"] = "tcp"
		doc["security"] = "none"
		doc["tls"] = true
		doc["cert_path"] = p.NodeCertPath
		doc["key_path"] = p.NodeKeyPath
		doc["server_name"] = ServerName
	case TrojanWS:
		doc["network"] = "ws"
		doc["path"] = TrojanWSPath
		doc["host"] = ServerName
		doc["security"] = "none"
		doc["tls"] = true
		doc["cert_path"] = p.NodeCertPath
		doc["key_path"] = p.NodeKeyPath
		doc["server_name"] = ServerName
	case SS:
		doc["method"] = SSMethod
		doc["network"] = "tcp"
	case VMessWS:
		doc["network"] = "ws"
		doc["path"] = VMessWSPath
		doc["host"] = ServerName
		doc["security"] = "none"
	case Hy2:
		doc["cert_path"] = p.NodeCertPath
		doc["key_path"] = p.NodeKeyPath
		doc["server_name"] = ServerName
	case TUIC:
		doc["cert_path"] = p.NodeCertPath
		doc["key_path"] = p.NodeKeyPath
		doc["congestion_control"] = TUICCongestion
		doc["server_name"] = ServerName
	case AnyTLS:
		doc["cert_path"] = p.NodeCertPath
		doc["key_path"] = p.NodeKeyPath
		doc["server_name"] = ServerName
	default:
		return nil, "", fmt.Errorf("未知协议: %s", proto)
	}
	doc["server_port"] = p.Port(proto)
	doc["protocol"] = NodeType(proto)
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

// CAFile 返回 CA 证书的绝对路径。
func (p *Profile) CAFile() string {
	return filepath.Join(p.Dir, p.Cert.CAFile)
}
