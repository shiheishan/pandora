package kernel

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/aegispanel/nodeagent/core"
)

// 在线设备跟踪器的语义：同 IP 多连接按引用计数，设备上限按不同 IP 数判。
func TestOnlineDevicesRefCount(t *testing.T) {
	type step struct {
		leave bool
		ip    string
		want  bool // 只对 enter 有意义：是否放行
	}
	enter := func(ip string, want bool) step { return step{ip: ip, want: want} }
	leave := func(ip string) step { return step{leave: true, ip: ip} }
	const a, b, c = "203.0.113.1", "203.0.113.2", "203.0.113.3"
	cases := []struct {
		name  string
		limit int
		steps []step
		want  []string // 最后上报的在线 IP；nil 表示用户不在上报里
	}{
		{"同 IP 两条连接关一条仍在线", 0, []step{enter(a, true), enter(a, true), leave(a)}, []string{a}},
		{"关掉全部才离线", 0, []step{enter(a, true), enter(a, true), leave(a), leave(a)}, nil},
		{"同 IP 多连接不额外占名额", 1, []step{enter(a, true), enter(a, true), enter(a, true), enter(b, false)}, []string{a}},
		{"上限按不同 IP 数", 2, []step{enter(a, true), enter(b, true), enter(c, false), enter(b, true)}, []string{a, b}},
		{"同 IP 只关一条不腾名额", 1, []step{enter(a, true), enter(a, true), leave(a), enter(b, false)}, []string{a}},
		{"IP 全部断开才腾名额", 1, []step{enter(a, true), enter(a, true), leave(a), leave(a), enter(b, true)}, []string{b}},
		{"被拒的连接不登记", 1, []step{enter(a, true), enter(b, false), leave(a)}, nil},
		{"多余的 leave 不让计数变负", 0, []step{enter(a, true), leave(a), leave(a), enter(a, true), enter(a, true), leave(a)}, []string{a}},
		{"不限设备", 0, []step{enter(a, true), enter(b, true), enter(c, true)}, []string{a, b, c}},
		{"IPv4 映射地址与 IPv4 同一设备", 1, []step{enter("::ffff:"+a, true), enter(a, true), leave("::ffff:" + a)}, []string{a}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d onlineDevices
			user := core.User{ID: 42, DeviceLimit: tc.limit}
			for i, s := range tc.steps {
				if s.leave {
					d.leave(user, s.ip)
					continue
				}
				if got := d.enter(user, s.ip); got != s.want {
					t.Fatalf("第 %d 步 enter(%s)=%v，期望 %v", i, s.ip, got, s.want)
				}
			}
			got := d.snapshot()[user.ID]
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("在线 IP=%v，期望 %v", got, tc.want)
			}
			if tc.want == nil && len(d.byUser) != 0 {
				t.Fatalf("全部离线后仍留着 %d 个用户的记录", len(d.byUser))
			}
		})
	}
}

// 不同用户互不占名额；并发进出后不留任何记录（-race 下跑）。
func TestOnlineDevicesConcurrentNoLeak(t *testing.T) {
	var d onlineDevices
	const users, ips, rounds = 8, 4, 200
	var wg sync.WaitGroup
	for u := 0; u < users; u++ {
		for i := 0; i < ips; i++ {
			for k := 0; k < 3; k++ {
				wg.Add(1)
				go func(u, i int) {
					defer wg.Done()
					user := core.User{ID: int64(u), DeviceLimit: ips}
					ip := fmt.Sprintf("198.51.100.%d", i)
					for r := 0; r < rounds; r++ {
						if !d.enter(user, ip) {
							t.Errorf("用户 %d 的第 %d 个 IP 被拒：上限 %d 不该被同用户的其他 IP 占满", u, i, ips)
							return
						}
						_ = d.snapshot()
						d.leave(user, ip)
					}
				}(u, i)
			}
		}
	}
	wg.Wait()
	if got := d.snapshot(); len(got) != 0 || len(d.byUser) != 0 {
		t.Fatalf("全部离开后仍有在线记录：%v", got)
	}
}
