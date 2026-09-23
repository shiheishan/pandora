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
