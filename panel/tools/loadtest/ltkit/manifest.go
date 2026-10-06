package ltkit

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ManifestVersion 只在字段语义变化时加一；新增字段不加。
const ManifestVersion = 1

// Manifest 是 seed 造完数之后写下的全部「压测需要知道、但只有造数时才拿得到」的东西：
// 节点私钥与运行令牌、用户的订阅令牌与固定来源 IP。
//
// 里面有节点私钥与登录口令（都是虚构的、只活在压测库里），文件以 0600 落盘，
// 路径由调用方给，不要放进仓库目录。
type Manifest struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	// Label 是档位名（例如 5k / 10k / 15k / ci），只用于报告。
	Label    string `json:"label"`
	TenantID string `json:"tenant_id"`
	// UserPassword 是全部造数用户共用的门户口令（虚构）。
	UserPassword string         `json:"user_password"`
	PlanIDs      []string       `json:"plan_ids"`
	Users        []ManifestUser `json:"users"`
	Nodes        []ManifestNode `json:"nodes"`

	// RunID 是这一批造数的命名空间（邮箱、节点名、池与套餐代码里都带它），区分同一库里的多次 seed。
	RunID string `json:"run_id,omitempty"`
	// SubscribePathPrefix 是租户的订阅路径前缀：订阅拉取 URL = 公共网关根 + "/" + 前缀 + "/" + SubscribeToken。
	SubscribePathPrefix string `json:"subscribe_path_prefix,omitempty"`
	// PoolID / PlanVersionID 是这一批节点所在的池与用户订阅锁定的套餐版本（PlanIDs 里那个套餐的已发布版本）。
	PoolID        string `json:"pool_id,omitempty"`
	PlanVersionID string `json:"plan_version_id,omitempty"`
	// SeedTimings 是 seed 各阶段的墙钟耗时，按执行顺序排列，最后一项是 total。
	SeedTimings []SeedTiming `json:"seed_timings,omitempty"`
}

// SeedTiming 是 seed 一个阶段的耗时：Count 是该阶段处理的对象数（用户、节点、管理请求……）。
type SeedTiming struct {
	Phase   string  `json:"phase"`
	Count   int     `json:"count"`
	Seconds float64 `json:"seconds"`
}

type ManifestUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	// SubscribeToken 是订阅链接里的那段令牌，拼成 /api/v1/client/subscribe?token= 之类由 users 决定。
	SubscribeToken string `json:"subscribe_token"`
	// RealIP 是这个模拟用户固定的来源地址，经 X-Real-IP 带给面板（虚构网段）。
	RealIP string `json:"real_ip"`
	// SubscriptionID / NodeUID 是这位用户唯一一条生效订阅的主键与对节点暴露的整数编号（UniProxy 用户列表的 id）。
	SubscriptionID string `json:"subscription_id,omitempty"`
	NodeUID        int64  `json:"node_uid,omitempty"`
}

type ManifestNode struct {
	ID       string `json:"id"`
	NodeType string `json:"node_type"`
	// RuntimeToken 是 UniProxy 兼容通道的节点令牌。
	RuntimeToken string `json:"runtime_token"`
	// PrivateKey 是节点 Ed25519 私钥（标准 base64，64 字节），签 /v1/nodes/* 请求。
	PrivateKey string `json:"private_key"`
	// Serial 是接入身份的序号，签名头不带它，留给排障。
	Serial int `json:"serial"`
	// ConfigKeyID / ConfigPublicKey 是面板配置签名公钥，模拟节点按 pdnd 的做法验配置签名。
	ConfigKeyID     string `json:"config_key_id"`
	ConfigPublicKey string `json:"config_public_key"`
	// Name 是节点名（带 seed 的命名空间），ServerID 是它所在的服务器。
	Name     string `json:"name,omitempty"`
	ServerID string `json:"server_id,omitempty"`
	// RealIP 是节点所在服务器登记的虚构公网地址（203.0.113.0/24），模拟节点经 X-Real-IP 带给面板：
	// 生产里每个节点一个来源 IP，nginx 的每 IP 限流按节点各算各的；不带的话两百个节点挤在压测机一个地址上。
	RealIP string `json:"real_ip,omitempty"`
}

func LoadManifest(path string) (*Manifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("manifest %s: version %d, want %d", path, m.Version, ManifestVersion)
	}
	return &m, nil
}

func (m *Manifest) Save(path string) error {
	m.Version = ManifestVersion
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
