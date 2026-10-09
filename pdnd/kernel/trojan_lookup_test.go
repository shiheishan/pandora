package kernel

import (
	"fmt"
	"testing"

	"github.com/aegispanel/nodeagent/core"
)

func newTrojanLookupAdapter(t testing.TB, n int) *trojanAdapter {
	t.Helper()
	a := &trojanAdapter{users: make(map[string]trojanUser)}
	users := make([]core.User, n)
	for i := range users {
		users[i] = core.User{ID: int64(i + 1), UUID: fmt.Sprintf("trojan-lookup-%d", i)}
	}
	if err := a.AddUsers(users); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTrojanLookupByProof(t *testing.T) {
	a := newTrojanLookupAdapter(t, 100)
	if u, ok := a.lookupUser(trojanPasswordProof("trojan-lookup-42")); !ok || u.ID != 43 {
		t.Fatalf("user=%+v ok=%v", u, ok)
	}
	if _, ok := a.lookupUser(trojanPasswordProof("nobody")); ok {
		t.Fatal("不在名单里的口令通过了")
	}
	// 口令哈希的大小写不同即不同键（与原先逐个精确比较同一口径）。
	upper := []byte(trojanPasswordProof("trojan-lookup-42"))
	for i, c := range upper {
		if c >= 'a' && c <= 'f' {
			upper[i] = c - 32
		}
	}
	if _, ok := a.lookupUser(string(upper)); ok {
		t.Fatal("大写摘要不该命中")
	}
}

func BenchmarkTrojanLookup5000Users(b *testing.B) {
	a := newTrojanLookupAdapter(b, 5000)
	proof := trojanPasswordProof("trojan-lookup-4999")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := a.lookupUser(proof); !ok {
			b.Fatal("miss")
		}
	}
}
