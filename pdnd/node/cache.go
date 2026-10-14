package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// 落盘缓存：面板不可达（或身份已吊销）时重启节点照常服务。
//
// 每个节点把「最后一份装上的配置」与「最后一份名单」原子写到状态目录（文件 0600，目录 0700）。
// 冷启动先装缓存、再连面板，Hello 报缓存里的版本，面板只发不同的部分；面板没回来时
// 节点就一直用缓存服务（心跳报 serving_cached_config）。
//
// 没有签名，靠目录权限保护；读回时按 content_sha256 重新核对配置内容，对不上丢弃。
// 缓存按服务器身份归属：换了身份（server_id 不同）的缓存整个丢弃。
//
// 布局：<root>/nodes/server.json 记归属与节点顺序；<root>/nodes/<节点>/config.json、users.json。

// DefaultCacheDir 是落盘缓存的缺省根目录，与 systemd 单元的 StateDirectory 一致。
const DefaultCacheDir = "/var/lib/pandora-native"

const cacheFormat = 2

type serverCache struct {
	dir      string
	serverID string
}

type nodeCache struct {
	dir    string
	nodeID string
}

type cachedServerFile struct {
	Format   int       `json:"format"`
	ServerID string    `json:"server_id"`
	Nodes    []string  `json:"nodes"`
	SavedAt  time.Time `json:"saved_at"`
}

type cachedConfigFile struct {
	Format        int    `json:"format"`
	NodeID        string `json:"node_id"`
	NodeType      string `json:"node_type"`
	ContentSHA256 string `json:"content_sha256"`
	// ConfigJSON 按 base64 存原字节：摘要按原字节算，重新编码 JSON 会对不上。
	ConfigJSON []byte    `json:"config_json"`
	SavedAt    time.Time `json:"saved_at"`
}

type cachedUser struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	SpeedLimit  int    `json:"speed_limit"`
	DeviceLimit int    `json:"device_limit"`
}

type cachedUsersFile struct {
	Format  int          `json:"format"`
	NodeID  string       `json:"node_id"`
	Version string       `json:"version"`
	Users   []cachedUser `json:"users"`
	SavedAt time.Time    `json:"saved_at"`
}

func newServerCache(root, serverID string) *serverCache {
	return &serverCache{dir: filepath.Join(root, "nodes"), serverID: serverID}
}

func (c *serverCache) node(id string) *nodeCache {
	return &nodeCache{dir: filepath.Join(c.dir, safePathSegment(id)), nodeID: id}
}

func (c *serverCache) path() string { return filepath.Join(c.dir, "server.json") }

// saveOrder 记下归属与面板下发的节点顺序（冷启动按它装，同机端口冲突时赢家不变）。
func (c *serverCache) saveOrder(ids []string) error {
	return writeJSON(c.dir, c.path(), cachedServerFile{
		Format: cacheFormat, ServerID: c.serverID, Nodes: ids, SavedAt: time.Now().UTC(),
	})
}

// loadOrder 读出缓存的节点顺序；格式或身份不符时整个缓存目录丢弃。
func (c *serverCache) loadOrder() ([]string, error) {
	var f cachedServerFile
	if err := readJSON(c.path(), &f); err != nil {
		return nil, err
	}
	if f.Format != cacheFormat || f.ServerID != c.serverID {
		_ = os.RemoveAll(c.dir)
		return nil, errors.New("落盘缓存属于另一个身份或格式不符，已丢弃")
	}
	return f.Nodes, nil
}

func (c *nodeCache) configPath() string { return filepath.Join(c.dir, "config.json") }
func (c *nodeCache) usersPath() string  { return filepath.Join(c.dir, "users.json") }

// saveConfig 在配置装上之后落盘（原字节与摘要一起）。
func (c *nodeCache) saveConfig(nc *nodev1.NodeConfig) error {
	return writeJSON(c.dir, c.configPath(), cachedConfigFile{
		Format: cacheFormat, NodeID: c.nodeID, NodeType: nc.GetNodeType(),
		ContentSHA256: strings.ToLower(nc.GetContentSha256()), ConfigJSON: nc.GetConfigJson(),
		SavedAt: time.Now().UTC(),
	})
}

// loadConfig 读回缓存的配置；格式、节点或摘要不符时删掉这个文件（以免每次启动都再试一遍）。
func (c *nodeCache) loadConfig() (*nodev1.NodeConfig, error) {
	var f cachedConfigFile
	if err := readJSON(c.configPath(), &f); err != nil {
		return nil, err
	}
	if f.Format != cacheFormat || f.NodeID != c.nodeID || contentDigest(f.ConfigJSON) != f.ContentSHA256 {
		_ = os.Remove(c.configPath())
		return nil, errors.New("落盘配置缓存格式、节点或摘要不符，已丢弃")
	}
	return &nodev1.NodeConfig{
		NodeId: f.NodeID, NodeType: f.NodeType, ConfigJson: f.ConfigJSON, ContentSha256: f.ContentSHA256,
	}, nil
}

// saveUsers 落盘名单与它的版本。
func (c *nodeCache) saveUsers(version string, users map[int64]core.User) error {
	out := make([]cachedUser, 0, len(users))
	for _, u := range users {
		out = append(out, cachedUser{ID: u.ID, UUID: u.UUID, SpeedLimit: u.SpeedLimit, DeviceLimit: u.DeviceLimit})
	}
	return writeJSON(c.dir, c.usersPath(), cachedUsersFile{
		Format: cacheFormat, NodeID: c.nodeID, Version: version, Users: out, SavedAt: time.Now().UTC(),
	})
}

// loadUsers 读回缓存的名单。
func (c *nodeCache) loadUsers() (version string, users []core.User, err error) {
	var f cachedUsersFile
	if err := readJSON(c.usersPath(), &f); err != nil {
		return "", nil, err
	}
	if f.Format != cacheFormat || f.NodeID != c.nodeID {
		return "", nil, errors.New("落盘名单缓存格式或节点不符")
	}
	users = make([]core.User, 0, len(f.Users))
	for _, u := range f.Users {
		users = append(users, core.User{ID: u.ID, UUID: u.UUID, SpeedLimit: u.SpeedLimit, DeviceLimit: u.DeviceLimit})
	}
	return f.Version, users, nil
}

// remove 删掉这个节点的缓存（节点已不在面板配置里）。
func (c *nodeCache) remove() { _ = os.RemoveAll(c.dir) }

func writeJSON(dir, path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return panel.WriteFileAtomic(path, body, 0o600)
}

func readJSON(path string, v any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// flushUsersCache 把有变化的名单落盘。名单变更（含增量）可能很密，不逐条写，
// 每个上报节拍与退出时各写一次。
func (s *Server) flushUsersCache() {
	for _, n := range s.nodes {
		if !n.usersDirty || !n.started || n.cache == nil {
			continue
		}
		if err := n.cache.saveUsers(n.userVersion, n.known); err != nil {
			n.log.Warn("名单落盘失败（不影响服务，只是面板不可达时重启无法用缓存）", "err", err)
			continue
		}
		n.usersDirty = false
	}
}

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// safePathSegment 把节点 id 变成安全的目录名：合规的原样用，否则用哈希。
func safePathSegment(s string) string {
	if safeSegment.MatchString(s) && s != "." && s != ".." {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "node-" + hex.EncodeToString(sum[:])[:24]
}
