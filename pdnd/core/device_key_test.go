package core

import "testing"

// 面板 node/service 的 deviceKey 与这里同一口径，它的 TestDeviceKey（metrics_test.go）自带同一组用例，
// 两边不互相读，改一边要同步另一边。保持「"输入": "期望",」一行一条的写法。
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
