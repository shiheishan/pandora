// [INPUT]: 依赖 encoding/json 与 os 读 seed 写下的 manifest 原文
// [OUTPUT]: 对外提供 包内的 manifestExtras、readManifestExtras（订阅路径前缀 subscribe_path_prefix、每个用户的 subscription_id）
// [POS]: tools/loadtest/userload 读 manifest 新增字段的本地视图：ltkit.Manifest 由总协调维护、这里不改它，seed 后加的字段按 JSON 名在本地结构体里读；字段缺失时 users 回落到经门户接口探测
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package userload

import (
	"encoding/json"
	"fmt"
	"os"
)

// manifestExtras 是 seed 在 ManifestVersion 1 上「只加不改」的字段里 users 用得到的两项。
// 订阅 URL = 公共网关根 + "/" + subscribe_path_prefix + "/" + subscribe_token。
type manifestExtras struct {
	SubscribePathPrefix string `json:"subscribe_path_prefix"`
	Users               []struct {
		ID             string `json:"id"`
		SubscriptionID string `json:"subscription_id"`
	} `json:"users"`
}

// readManifestExtras 读 manifest 原文里的新增字段；path 为空（测试直接传 Manifest）时返回空视图。
func readManifestExtras(path string) (manifestExtras, error) {
	var x manifestExtras
	if path == "" {
		return x, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return x, err
	}
	if err := json.Unmarshal(body, &x); err != nil {
		return x, fmt.Errorf("parse manifest extras %s: %w", path, err)
	}
	return x, nil
}

// subscriptionIDs 把 manifest 里的订阅 id 按用户 id 索引，空值不进表。
func (x manifestExtras) subscriptionIDs() map[string]string {
	out := make(map[string]string, len(x.Users))
	for _, u := range x.Users {
		if u.SubscriptionID != "" {
			out[u.ID] = u.SubscriptionID
		}
	}
	return out
}
