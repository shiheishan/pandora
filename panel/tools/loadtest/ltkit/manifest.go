// [INPUT]: 依赖 encoding/json 与 os 读写清单文件
// [OUTPUT]: 对外提供 Manifest、ManifestUser、ManifestNode、LoadManifest、(*Manifest).Save
// [POS]: tools/loadtest/ltkit 的造数清单：seed 写、nodes/users/burst 读，是四个子命令之间唯一的数据契约
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
}

type ManifestUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	// SubscribeToken 是订阅链接里的那段令牌，拼成 /api/v1/client/subscribe?token= 之类由 users 决定。
	SubscribeToken string `json:"subscribe_token"`
	// RealIP 是这个模拟用户固定的来源地址，经 X-Real-IP 带给面板（虚构网段）。
	RealIP string `json:"real_ip"`
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
