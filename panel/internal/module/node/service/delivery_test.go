package service

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
)

func TestPoolAdmits(t *testing.T) {
	g1 := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	g2 := uuid.MustParse("10000000-0000-0000-0000-000000000002")

	cases := []struct {
		name       string
		poolGroups []uuid.UUID
		userGroup  *uuid.UUID
		want       bool
	}{
		{name: "名单空且有组", poolGroups: nil, userGroup: &g1, want: true},
		{name: "名单空且默认组", poolGroups: []uuid.UUID{}, userGroup: nil, want: true},
		{name: "名单含g1用户g1", poolGroups: []uuid.UUID{g1}, userGroup: &g1, want: true},
		{name: "名单含g1g2用户g2", poolGroups: []uuid.UUID{g1, g2}, userGroup: &g2, want: true},
		{name: "名单含g1用户g2", poolGroups: []uuid.UUID{g1}, userGroup: &g2, want: false},
		{name: "名单含g1用户默认组", poolGroups: []uuid.UUID{g1}, userGroup: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := poolAdmits(tc.poolGroups, tc.userGroup); got != tc.want {
				t.Fatalf("poolAdmits() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRosterLimits(t *testing.T) {
	throttle := 1000
	maxDev := 3
	v := catalogsvc.Version{
		ThrottleKbps: intPtr(throttle),
		MaxDevices:   intPtr(maxDev),
	}

	cases := []struct {
		name           string
		subDeviceLimit *int
		ver            catalogsvc.Version
		wantSpeed      uint32
		wantDevices    uint32
	}{
		{
			name:           "订阅覆盖nil",
			subDeviceLimit: nil,
			ver:            v,
			wantSpeed:      1000,
			wantDevices:    3,
		},
		{
			name:           "订阅覆盖0",
			subDeviceLimit: intPtr(0),
			ver:            v,
			wantSpeed:      1000,
			wantDevices:    0,
		},
		{
			name:           "订阅覆盖5",
			subDeviceLimit: intPtr(5),
			ver:            v,
			wantSpeed:      1000,
			wantDevices:    5,
		},
		{
			name:           "版本无限速与设备订阅nil",
			subDeviceLimit: nil,
			ver:            catalogsvc.Version{ThrottleKbps: nil, MaxDevices: nil},
			wantSpeed:      0,
			wantDevices:    0,
		},
		{
			name:           "负数覆盖设备为0",
			subDeviceLimit: intPtr(-1),
			ver:            v,
			wantSpeed:      1000,
			wantDevices:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			speed, devices := rosterLimits(tc.subDeviceLimit, tc.ver)
			if speed != tc.wantSpeed || devices != tc.wantDevices {
				t.Fatalf("rosterLimits() = (%d, %d), want (%d, %d)", speed, devices, tc.wantSpeed, tc.wantDevices)
			}
		})
	}
}

func TestRosterVersion(t *testing.T) {
	uA := uuid.MustParse("20000000-0000-0000-0000-00000000000a")
	uB := uuid.MustParse("20000000-0000-0000-0000-00000000000b")

	userA := RosterUser{UID: 10, UUID: uA, SpeedLimitKbps: 100, DeviceLimit: 2}
	userB := RosterUser{UID: 20, UUID: uB, SpeedLimitKbps: 0, DeviceLimit: 1}

	t.Run("空名单格式", func(t *testing.T) {
		empty := rosterVersion(nil)
		empty2 := rosterVersion([]RosterUser{})
		if empty != empty2 {
			t.Fatalf("nil 与空切片版本不同: %q vs %q", empty, empty2)
		}
		if len(empty) != 3+64 {
			t.Fatalf("版本总长 %d, want %d", len(empty), 3+64)
		}
		if !strings.HasPrefix(empty, rosterVersionPrefix) {
			t.Fatalf("版本应以 %q 开头, got %q", rosterVersionPrefix, empty)
		}
	})

	t.Run("顺序无关且不修改调用方切片", func(t *testing.T) {
		ordered := []RosterUser{userA, userB}
		shuffled := []RosterUser{userB, userA}
		before := slices.Clone(shuffled)

		vOrdered := rosterVersion(ordered)
		vShuffled := rosterVersion(shuffled)

		if vOrdered != vShuffled {
			t.Fatalf("不同输入顺序版本应相同: %q vs %q", vOrdered, vShuffled)
		}
		if !reflect.DeepEqual(shuffled, before) {
			t.Fatalf("rosterVersion 修改了调用方切片: before %v after %v", before, shuffled)
		}
	})

	t.Run("字段变化则版本变", func(t *testing.T) {
		base := []RosterUser{userA}
		vBase := rosterVersion(base)

		mutations := []RosterUser{
			{UID: 11, UUID: uA, SpeedLimitKbps: 100, DeviceLimit: 2},
			{UID: 10, UUID: uA, SpeedLimitKbps: 101, DeviceLimit: 2},
			{UID: 10, UUID: uA, SpeedLimitKbps: 100, DeviceLimit: 3},
			{UID: 10, UUID: uB, SpeedLimitKbps: 100, DeviceLimit: 2},
		}
		for i, m := range mutations {
			if rosterVersion([]RosterUser{m}) == vBase {
				t.Fatalf("变异 %d 应与基准版本不同", i)
			}
		}
	})

	t.Run("增删用户版本变", func(t *testing.T) {
		one := rosterVersion([]RosterUser{userA})
		two := rosterVersion([]RosterUser{userA, userB})
		if one == two {
			t.Fatal("单用户与双用户版本应不同")
		}
		if rosterVersion([]RosterUser{userB}) == one {
			t.Fatal("换用户后版本应不同")
		}
	})

	t.Run("规范编码钉死", func(t *testing.T) {
		uid := uuid.MustParse("00000000-0000-0000-0000-000000000001")
		users := []RosterUser{{
			UID:            1,
			UUID:           uid,
			SpeedLimitKbps: 0,
			DeviceLimit:    0,
		}}
		line := "1,00000000-0000-0000-0000-000000000001,0,0\n"
		sum := sha256.Sum256([]byte(line))
		want := rosterVersionPrefix + hex.EncodeToString(sum[:])
		if got := rosterVersion(users); got != want {
			t.Fatalf("rosterVersion() = %q, want %q", got, want)
		}
	})
}

func TestPreferFresh(t *testing.T) {
	cand := func(name string, fresh, degraded bool) deliveryCandidate {
		return deliveryCandidate{
			node:     render.Node{Name: name},
			fresh:    fresh,
			degraded: degraded,
		}
	}
	names := func(nodes []render.Node) []string {
		out := make([]string, len(nodes))
		for i, n := range nodes {
			out[i] = n.Name
		}
		return out
	}

	cases := []struct {
		name string
		in   []deliveryCandidate
		want []string
	}{
		{
			name: "全新鲜不降级",
			in: []deliveryCandidate{
				cand("a", true, false),
				cand("b", true, false),
				cand("c", true, false),
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "新鲜与超时混合",
			in: []deliveryCandidate{
				cand("a", true, false),
				cand("b", false, false),
				cand("c", true, false),
			},
			want: []string{"a", "c"},
		},
		{
			name: "全超时",
			in: []deliveryCandidate{
				cand("a", false, false),
				cand("b", false, false),
			},
			want: []string{"a", "b"},
		},
		{
			name: "降级新鲜与不降级超时",
			in: []deliveryCandidate{
				cand("deg_fresh", true, true),
				cand("ok_stale", false, false),
				cand("deg_stale", false, true),
			},
			want: []string{"ok_stale"},
		},
		{
			name: "全降级部分新鲜",
			in: []deliveryCandidate{
				cand("a", true, true),
				cand("b", false, true),
				cand("c", true, true),
			},
			want: []string{"a", "c"},
		},
		{
			name: "全降级全超时",
			in: []deliveryCandidate{
				cand("a", false, true),
				cand("b", false, true),
			},
			want: []string{"a", "b"},
		},
		{
			name: "空输入",
			in:   nil,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := preferFresh(tc.in)
			if tc.name == "空输入" {
				if out == nil {
					t.Fatal("空输入应返回非 nil 切片")
				}
				if len(out) != 0 {
					t.Fatalf("空输入长度 %d, want 0", len(out))
				}
				return
			}
			if got := names(out); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("节点名序列 %v, want %v", got, tc.want)
			}
		})
	}
}

func intPtr(n int) *int { return &n }
