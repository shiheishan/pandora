package realtime

import (
	"slices"
	"testing"
)

// 变更推到哪些频道是权限边界，也决定扇出：带 user_id 的只给本人，节点变更只给后台，
// 其余推全租户；管理端总抄一份。
func TestChannelsForRoutesByOwnerAndAudience(t *testing.T) {
	const tenant = "t-1"
	for _, tc := range []struct {
		name string
		c    change
		want []string
	}{
		{"owned row goes to its owner and admin",
			change{Table: "orders", Tenant: tenant, User: "u-1"},
			[]string{ChannelUser(tenant, "u-1"), ChannelAdmin(tenant)}},
		{"shared catalog goes to the whole tenant",
			change{Table: "plans", Tenant: tenant},
			[]string{ChannelPublic(tenant), ChannelAdmin(tenant)}},
		{"node changes stay on the admin channel",
			change{Table: "nodes", Tenant: tenant, ID: "n-1"},
			[]string{ChannelAdmin(tenant)}},
	} {
		got := channelsFor(tc.c)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: channels=%v want %v", tc.name, got, tc.want)
		}
	}
	// 门户 SSE 只订阅公共频道与本人频道：节点变更不能出现在其中任何一个
	for _, ch := range channelsFor(change{Table: "nodes", Tenant: tenant}) {
		if ch == ChannelPublic(tenant) {
			t.Fatal("nodes.changed must not fan out to every portal connection")
		}
	}
}
