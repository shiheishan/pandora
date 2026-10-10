package nodefabric

import "testing"

// 版本号只应对「会影响节点行为的字段」敏感。
func TestUserSetVersionTracksMeaningfulChanges(t *testing.T) {
	base := []ProxyUser{
		{ID: 1, UUID: "u1", SpeedLimit: 100, DeviceLimit: 3},
		{ID: 2, UUID: "u2", SpeedLimit: 0, DeviceLimit: 0},
	}
	v := UserSetVersion(base)

	// 顺序变了不算变：SQL 的 ORDER BY 换一下不该让所有节点重新拉一遍
	shuffled := []ProxyUser{base[1], base[0]}
	if UserSetVersion(shuffled) != v {
		t.Error("仅顺序不同却算出了不同的版本")
	}

	for name, changed := range map[string][]ProxyUser{
		"改限速":    {{ID: 1, UUID: "u1", SpeedLimit: 200, DeviceLimit: 3}, base[1]},
		"改设备数":   {{ID: 1, UUID: "u1", SpeedLimit: 100, DeviceLimit: 5}, base[1]},
		"改 UUID": {{ID: 1, UUID: "other", SpeedLimit: 100, DeviceLimit: 3}, base[1]},
		"少一个人":   {base[0]},
		"多一个人":   append(append([]ProxyUser{}, base...), ProxyUser{ID: 3, UUID: "u3"}),
	} {
		if UserSetVersion(changed) == v {
			t.Errorf("%s：版本号没变，节点端会一直拿 304，改动永远同步不下去", name)
		}
	}
}

// 拼接歧义：没有分隔符的话 (1,"23") 和 (12,"3") 会算出同一个摘要。
func TestUserSetVersionHasNoConcatenationCollision(t *testing.T) {
	a := []ProxyUser{{ID: 1, UUID: "23"}}
	b := []ProxyUser{{ID: 12, UUID: "3"}}
	if UserSetVersion(a) == UserSetVersion(b) {
		t.Error("不同的用户算出了同一个版本号——字段拼接缺分隔符")
	}
}

func TestDiffUsers(t *testing.T) {
	old := []ProxyUser{
		{ID: 1, UUID: "u1", SpeedLimit: 100},
		{ID: 2, UUID: "u2"},
		{ID: 3, UUID: "u3"},
	}
	now := []ProxyUser{
		{ID: 1, UUID: "u1", SpeedLimit: 200}, // 改了限速
		{ID: 2, UUID: "u2"},                  // 没动
		{ID: 4, UUID: "u4"},                  // 新增
	}
	d := DiffUsers(old, now)

	// 改动和新增都进 Added：对节点端来说处理方式一样，都是按这份覆盖
	if len(d.Added) != 2 || d.Added[0].ID != 1 || d.Added[1].ID != 4 {
		t.Errorf("Added = %+v，期望 [1（改过） 4（新增）]", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0] != 3 {
		t.Errorf("Removed = %v，期望 [3]", d.Removed)
	}
	if d.Empty() {
		t.Error("有差异却报告为空")
	}
}

// 没变化时差异必须为空，否则 WS 会推一堆无意义的消息。
func TestDiffUsersEmptyWhenUnchanged(t *testing.T) {
	users := []ProxyUser{{ID: 1, UUID: "u1", SpeedLimit: 50, DeviceLimit: 2}}
	if d := DiffUsers(users, users); !d.Empty() {
		t.Errorf("相同列表算出了差异：%+v", d)
	}
	// 顺序不同也该算没变
	two := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}
	rev := []ProxyUser{two[1], two[0]}
	if d := DiffUsers(two, rev); !d.Empty() {
		t.Errorf("仅顺序不同算出了差异：%+v", d)
	}
}

// 全空到有人、有人到全空这两个边界。
func TestDiffUsersHandlesEmptySides(t *testing.T) {
	users := []ProxyUser{{ID: 1, UUID: "u1"}}
	if d := DiffUsers(nil, users); len(d.Added) != 1 || len(d.Removed) != 0 {
		t.Errorf("从空到有：%+v", d)
	}
	if d := DiffUsers(users, nil); len(d.Added) != 0 || len(d.Removed) != 1 {
		t.Errorf("从有到空：%+v", d)
	}
}

// 同一用户 ID 换了凭据（重置订阅只换 proxy_uuid、node_uid 不变）：增量必须同时给
// Removed(ID) 与 Added(新记录)。只给 Added 的话，节点端按「只加不替换」打补丁时旧凭据
// 留在放行名单里，用户以为作废的旧链接照样能用、照样计到他头上。
//
// 面板名单里每个用户只有一份节点凭据（proxy_uuid）：vless / vmess / tuic 当 uuid，
// trojan / ss / hysteria2 / anytls / socks 等当 password，都是这同一个值，所以这里
// 一条用例覆盖全部协议。节点级密钥（obfs、shadowtls、ss2022 服务端口令）随配置下发，
// 换了就重建入站、走全量，不经这条增量路径。
func TestDiffUsersRotatedCredentialRemovesOld(t *testing.T) {
	old := []ProxyUser{
		{ID: 1, UUID: "u1-old", SpeedLimit: 100, DeviceLimit: 2},
		{ID: 2, UUID: "u2", SpeedLimit: 100},
	}
	now := []ProxyUser{
		{ID: 1, UUID: "u1-new", SpeedLimit: 100, DeviceLimit: 2}, // 重置订阅：只换凭据
		{ID: 2, UUID: "u2", SpeedLimit: 200},                     // 只改限速：凭据没变
	}
	d := DiffUsers(old, now)
	if len(d.Removed) != 1 || d.Removed[0] != 1 {
		t.Fatalf("Removed = %v，期望 [1]：换了凭据的用户要先删旧凭据", d.Removed)
	}
	if len(d.Added) != 2 || d.Added[0] != now[0] || d.Added[1] != now[1] {
		t.Fatalf("Added = %+v，期望新凭据与改过限速的记录", d.Added)
	}
}

// 增量打在起点名单上，必须得到终点名单的那一组凭据——按最弱的节点端语义打：
// 先按 Removed 的 ID 删掉名下凭据，再把 Added 的凭据加进去（只加、不按 ID 替换）。
// 面板这一侧单独就要挡住「旧凭据仍放行」，不依赖节点端兜底。
func TestDiffUsersAppliedOnWeakestNodeYieldsTargetCredentials(t *testing.T) {
	cases := map[string]struct{ old, now []ProxyUser }{
		"重置订阅": {
			old: []ProxyUser{{ID: 1, UUID: "a-old"}, {ID: 2, UUID: "b"}},
			now: []ProxyUser{{ID: 1, UUID: "a-new"}, {ID: 2, UUID: "b"}},
		},
		"重置订阅且改限速": {
			old: []ProxyUser{{ID: 1, UUID: "a-old", SpeedLimit: 1}},
			now: []ProxyUser{{ID: 1, UUID: "a-new", SpeedLimit: 2}},
		},
		"一批人里有人换凭据、有人走、有人来": {
			old: []ProxyUser{{ID: 1, UUID: "a-old"}, {ID: 2, UUID: "b"}, {ID: 3, UUID: "c"}},
			now: []ProxyUser{{ID: 1, UUID: "a-new"}, {ID: 2, UUID: "b", DeviceLimit: 3}, {ID: 4, UUID: "d"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// 节点手上的名单：凭据 → 用户 ID
			node := make(map[string]int64, len(tc.old))
			for _, u := range tc.old {
				node[u.UUID] = u.ID
			}
			d := DiffUsers(tc.old, tc.now)
			drop := make(map[int64]bool, len(d.Removed))
			for _, id := range d.Removed {
				drop[id] = true
			}
			for uuid, id := range node {
				if drop[id] {
					delete(node, uuid)
				}
			}
			for _, u := range d.Added {
				node[u.UUID] = u.ID
			}
			want := make(map[string]int64, len(tc.now))
			for _, u := range tc.now {
				want[u.UUID] = u.ID
			}
			if len(node) != len(want) {
				t.Fatalf("打完增量节点放行 %v，期望 %v", node, want)
			}
			for uuid, id := range want {
				if got, ok := node[uuid]; !ok || got != id {
					t.Fatalf("打完增量节点放行 %v，期望 %v", node, want)
				}
			}
		})
	}
}
