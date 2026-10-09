package nodefabric

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 面板与 pdnd 的设备键必须同一口径：直接读 pdnd core 的 TestDeviceKey 用例表，
// 同输入同输出（pdnd/core/device_key_test.go）。改那张表或任一边的实现，这里都会对上。
func TestAliveDeviceKeyMatchesPdndDeviceKey(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "pdnd", "core", "device_key_test.go"))
	if err != nil {
		t.Fatalf("read pdnd device key cases: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func TestDeviceKey")
	if start < 0 {
		t.Fatal("pdnd TestDeviceKey not found; update this parity test")
	}
	body = body[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	// "输入": "期望",
	row := regexp.MustCompile(`"([^"]*)":\s*"([^"]*)",`)
	rows := row.FindAllStringSubmatch(body, -1)
	if len(rows) < 8 {
		t.Fatalf("parsed only %d pdnd cases; the table format changed, update this parity test", len(rows))
	}
	for _, r := range rows {
		if got := aliveDeviceKey(r[1]); got != r[2] {
			t.Errorf("pdnd case %q → %q, panel aliveDeviceKey = %q", r[1], r[2], got)
		}
	}
}

// 归一在去重之前：同一 /64 的两个地址、新节点报的 /64 键、旧节点报的原始地址只出一行；
// IPv4 映射地址与 IPv4 同一行。
func TestAliveRowsNormalizesBeforeDedup(t *testing.T) {
	uids, hashes := aliveRows(map[string][]string{
		"7": {"2001:db8:1:2::10", "2001:db8:1:2:a:b:c:d", "2001:db8:1:2::/64", "2001:db8:1:3::10"},
		"8": {"::ffff:203.0.113.7", "203.0.113.7"},
	})
	if len(uids) != 3 {
		t.Fatalf("rows = %v; want 2 devices for uid 7 and 1 for uid 8", uids)
	}
	want := map[string]bool{}
	for _, key := range []string{"2001:db8:1:2::/64", "2001:db8:1:3::/64", "203.0.113.7"} {
		sum := sha256.Sum256([]byte(key))
		want[string(sum[:])] = true
	}
	for _, h := range hashes {
		if !want[string(h)] {
			t.Fatalf("unexpected hash %x: rows must hash the device key", h)
		}
	}
}
