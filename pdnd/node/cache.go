package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// 落盘缓存：面板不可达时重启节点照常服务。
//
// 原先 pdnd 只落盘 identity.json，节点在面板不可达时重启（升级、崩溃、VPS
// 重启）就彻底停服，直到面板恢复。现在每个节点把「最后一份装上的配置」与
// 「最后一份用户名单」原子写到状态目录（0600），启动时面板不可达才拿出来用，
// 面板一回来就被面板的当前版本取代。
//
// 目录按「面板 + 节点」分开：<root>/panel-<面板地址哈希>/<节点 ID>/
// （panel.IdentityStateDir），为一个进程接多个面板留好位置，也避免两个面板的
// 同号节点互相覆盖。身份文件需要迁出只读目录时也落在这里。
//
// 签名通道的配置缓存连签名一起存，读回时用钉住的配置公钥重新验签
// （VerifyCachedConfig：只不查投递窗口）；验不过、节点身份对不上、通道对不上，
// 一律丢弃缓存，fail-closed。兼容通道本来就没有签名，缓存与面板下发同等可信，
// 靠目录权限保护。

// DefaultCacheDir 是落盘缓存的缺省根目录，与 systemd 单元的 StateDirectory 一致。
const DefaultCacheDir = "/var/lib/pandora-native"

const cacheFormat = 1

const (
	channelSigned = "signed"
	channelCompat = "compat"
)

type nodeCache struct {
	dir    string
	nodeID string
}

type cachedConfigFile struct {
	Format  int                 `json:"format"`
	NodeID  string              `json:"node_id"`
	Channel string              `json:"channel"`
	Signed  *panel.SignedConfig `json:"signed,omitempty"`
	Compat  json.RawMessage     `json:"compat,omitempty"`
	// ETag 是兼容通道的配置 ETag：装回之后面板仍是这一版就回 304。
	ETag    string    `json:"etag,omitempty"`
	SavedAt time.Time `json:"saved_at"`
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
	Users   []cachedUser `json:"users"`
	Version string       `json:"version,omitempty"`
	ETag    string       `json:"etag,omitempty"`
	SavedAt time.Time    `json:"saved_at"`
}

// SetCacheDir 启用落盘缓存，root 为空即用 DefaultCacheDir。
func (n *Node) SetCacheDir(root string) {
	if strings.TrimSpace(root) == "" {
		root = DefaultCacheDir
	}
	n.cache = &nodeCache{dir: panel.IdentityStateDir(root, n.client.BaseURL(), n.client.NodeID()), nodeID: n.client.NodeID()}
}

func (c *nodeCache) configPath() string { return filepath.Join(c.dir, "config.json") }
func (c *nodeCache) usersPath() string  { return filepath.Join(c.dir, "users.json") }

func (c *nodeCache) write(path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	return panel.WriteFileAtomic(path, body, 0o600)
}

func (c *nodeCache) read(path string, v any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// saveSignedConfigCache 在签名配置装上之后落盘（连签名一起）。
func (n *Node) saveSignedConfigCache(cfg *panel.SignedConfig) {
	if n.cache == nil || cfg == nil {
		return
	}
	n.writeCache(n.cache.configPath(), cachedConfigFile{
		Format: cacheFormat, NodeID: n.cache.nodeID, Channel: channelSigned, Signed: cfg, SavedAt: time.Now().UTC(),
	})
}

// saveCompatConfigCache 在兼容通道配置装上之后落盘。
func (n *Node) saveCompatConfigCache(cfg map[string]any) {
	if n.cache == nil {
		return
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		n.log.Warn("配置无法序列化，未落盘", "err", err)
		return
	}
	n.writeCache(n.cache.configPath(), cachedConfigFile{
		Format: cacheFormat, NodeID: n.cache.nodeID, Channel: channelCompat, Compat: raw,
		ETag: n.client.ConfigVersion(), SavedAt: time.Now().UTC(),
	})
}

// flushUsersCache 把有变化的用户名单落盘。用户变更（含事件流增量）可能很密，
// 不逐条写，每轮拉取末尾与退出时各写一次。
func (n *Node) flushUsersCache() {
	if n.cache == nil || !n.usersDirty || !n.started {
		return
	}
	users := make([]cachedUser, 0, len(n.known))
	for _, u := range n.known {
		users = append(users, cachedUser{ID: u.ID, UUID: u.UUID, SpeedLimit: u.SpeedLimit, DeviceLimit: u.DeviceLimit})
	}
	if n.writeCache(n.cache.usersPath(), cachedUsersFile{
		Format: cacheFormat, NodeID: n.cache.nodeID, Users: users, Version: n.userVersion,
		ETag: n.client.UsersVersion(), SavedAt: time.Now().UTC(),
	}) {
		n.usersDirty = false
	}
}

func (n *Node) writeCache(path string, v any) bool {
	if err := n.cache.write(path, v); err != nil {
		n.log.Warn("落盘缓存写入失败（不影响服务，只是面板不可达时重启无法用缓存）", "path", path, "err", err)
		return false
	}
	return true
}

// loadConfigFromCache 把缓存里的配置装上。签名通道重新验签，任何不符都丢弃缓存。
func (n *Node) loadConfigFromCache() error {
	if n.cache == nil {
		return errors.New("落盘缓存未启用")
	}
	var file cachedConfigFile
	if err := n.cache.read(n.cache.configPath(), &file); err != nil {
		return err
	}
	if file.Format != cacheFormat || file.NodeID != n.cache.nodeID {
		return n.discardConfigCache(fmt.Errorf("缓存格式或节点不符"))
	}
	if n.signed != nil {
		if file.Channel != channelSigned || file.Signed == nil {
			return n.discardConfigCache(errors.New("签名节点不接受非签名的配置缓存"))
		}
		if err := n.signed.VerifyCachedConfig(file.Signed); err != nil {
			return n.discardConfigCache(fmt.Errorf("配置缓存验签失败: %w", err))
		}
		if err := n.applySignedConfig(file.Signed); err != nil {
			return err
		}
		n.recordAppliedSignedConfig(file.Signed)
		return nil
	}
	if file.Channel != channelCompat || len(file.Compat) == 0 {
		return n.discardConfigCache(errors.New("配置缓存通道不符"))
	}
	var cfg map[string]any
	if err := json.Unmarshal(file.Compat, &cfg); err != nil {
		return n.discardConfigCache(err)
	}
	if err := n.applyConfig(cfg); err != nil {
		return err
	}
	n.client.SetConfigVersion(file.ETag)
	return nil
}

// discardConfigCache 删掉验不过的配置缓存（以免每次启动都再试一遍），返回原因。
func (n *Node) discardConfigCache(reason error) error {
	_ = os.Remove(n.cache.configPath())
	return fmt.Errorf("落盘配置缓存已丢弃: %w", reason)
}

// loadUsersFromCache 把缓存里的用户名单装进内核，并记下它的版本与 ETag：面板
// 回来时仍是这一版就回 304，不必再拉全量。
func (n *Node) loadUsersFromCache() error {
	if n.cache == nil {
		return errors.New("落盘缓存未启用")
	}
	var file cachedUsersFile
	if err := n.cache.read(n.cache.usersPath(), &file); err != nil {
		return err
	}
	if file.Format != cacheFormat || file.NodeID != n.cache.nodeID {
		return errors.New("用户缓存格式或节点不符")
	}
	users := make([]core.User, 0, len(file.Users))
	for _, u := range file.Users {
		users = append(users, core.User{ID: u.ID, UUID: u.UUID, SpeedLimit: u.SpeedLimit, DeviceLimit: u.DeviceLimit})
	}
	if err := n.applyUsers(users); err != nil {
		return err
	}
	n.usersDirty = false
	n.userVersion = file.Version
	n.client.SetUsersVersion(file.ETag)
	return nil
}
