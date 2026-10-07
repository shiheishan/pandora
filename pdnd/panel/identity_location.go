package panel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 身份文件放在服务可写的地方。
//
// 安装脚本把 identity.json 装在 /etc/pandora-native（root:pandora 0640），而
// systemd 单元是 ProtectSystem=strict、只放行 /var/lib/pandora-native 与
// /var/log/pandora-native 可写。面板一轮换配置签名密钥，RefreshConfigSigningKey
// 要把新公钥写回身份文件（SaveIdentity），在生产上必然失败——节点从此对新发布
// 一直验签失败。
//
// ResolveIdentityPath 在启动时检查：身份文件所在目录对本进程可写就原地用；不可写
// 就把它迁到状态目录 <stateDir>/panel-<面板哈希>/<节点 ID>/identity.json（与落盘
// 缓存同一个目录），以后读写都用那一份。旧位置不动，作只读回退。
//
// 已迁的副本与旧位置是同一个身份（同节点、同私钥，只是配置公钥可能已轮换）时
// 沿用副本——它更新；旧位置换成了另一个身份（重新接入）时以旧位置为准覆盖副本。

// IdentityStateDir 返回某个面板下某个节点的状态目录（落盘缓存、迁移后的身份都在这里）。
func IdentityStateDir(stateDir, panelURL, nodeID string) string {
	panelSum := sha256.Sum256([]byte(strings.TrimRight(strings.ToLower(strings.TrimSpace(panelURL)), "/")))
	return filepath.Join(stateDir, "panel-"+hex.EncodeToString(panelSum[:8]), SafePathSegment(nodeID))
}

// SafePathSegment 把节点 ID 变成安全的单段路径：不是 [A-Za-z0-9._-]{1,64}（或是
// . / ..）时改用它的哈希，不让配置里的值决定写到哪里。
func SafePathSegment(s string) string {
	ok := s != "" && len(s) <= 64 && s != "." && s != ".."
	for i := 0; ok && i < len(s); i++ {
		c := s[i]
		ok = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
	}
	if ok {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "node-" + hex.EncodeToString(sum[:12])
}

// ResolveIdentityPath 返回运行期该用的身份文件路径（见文件头）。身份文件不存在或
// 读不出时原样返回 path，交给调用方按原逻辑报错或走兼容通道。
func ResolveIdentityPath(path, stateDir, panelURL, nodeID string) (string, error) {
	source, err := LoadIdentity(path)
	if err != nil {
		return path, nil
	}
	if dirWritable(filepath.Dir(path)) {
		return path, nil
	}
	if strings.TrimSpace(stateDir) == "" {
		return path, errors.New("身份文件所在目录不可写，且没有状态目录可迁")
	}
	target := filepath.Join(IdentityStateDir(stateDir, panelURL, nodeID), "identity.json")
	if target == path {
		return path, nil
	}
	if migrated, err := LoadIdentity(target); err == nil && sameIdentity(migrated, source) {
		return target, nil
	}
	if err := SaveIdentity(target, source); err != nil {
		return path, fmt.Errorf("迁移身份文件到状态目录: %w", err)
	}
	return target, nil
}

// sameIdentity：同一节点、同一把节点私钥即同一个身份（配置公钥可能已轮换过）。
func sameIdentity(a, b *Identity) bool {
	return a.NodeID == b.NodeID && a.Serial == b.Serial &&
		bytes.Equal([]byte(a.PrivateKey), []byte(b.PrivateKey))
}

// dirWritable 实测能不能在 dir 里建文件（ProtectSystem、只读挂载、权限都算进去）。
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".pandora-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
