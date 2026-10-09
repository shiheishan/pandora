package core

import "testing"

// 面板的 aliveDeviceKey 读这张表对照（panel/internal/domain/nodefabric/alive_device_key_test.go），
// 保持「"输入": "期望",」一行一条的写法。
func TestDeviceKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"2001:db8:1:2::10":     "2001:db8:1:2::/64",
		"2001:db8:1:2:a:b:c:d": "2001:db8:1:2::/64",
		"2001:db8:1:3::10":     "2001:db8:1:3::/64",
		"fe80::1%eth0":         "fe80::/64",
		"::1":                  "::/64",
		"2001:db8:1:2::/64":    "2001:db8:1:2::/64",
		"unknown":              "unknown",
	} {
		if got := DeviceKey(in); got != want {
			t.Errorf("DeviceKey(%q)=%q，期望 %q", in, got, want)
		}
	}
}
