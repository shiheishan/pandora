package nodefabric

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestParseAppliedEffectiveReleaseIsStrict(t *testing.T) {
	const id = "aaaaaaaa-1111-4111-8111-11111111111b"
	if got, gen, ok := ParseAppliedEffectiveRelease(" " + id + "/7 "); !ok || got != id || gen != 7 {
		t.Fatalf("canonical value rejected: %q %d %v", got, gen, ok)
	}
	for _, bad := range []string{
		"", id, id + "/", id + "/0", id + "/-1", id + "/07", id + "/1/2",
		strings.ToUpper(id) + "/1", "00000000-0000-0000-0000-000000000000/1",
		id + "/9223372036854775808", "not-a-uuid/1",
	} {
		if _, _, ok := ParseAppliedEffectiveRelease(bad); ok {
			t.Fatalf("accepted non-canonical applied release %q", bad)
		}
	}
}

// 规范字节按「原始字节 + 存档哈希」记住：同一份只算一次；库里任何一样被改过就
// 重新校验，篡改照样被发现。
func TestReleaseMemoRecomputesOnTamper(t *testing.T) {
	var m releaseMemo
	raw := []byte(`{"b": 1, "a": 2}`)
	canonical := []byte(`{"a":2,"b":1}`)
	sum := sha256.Sum256(canonical)

	got, ok, err := m.canonicalVerified("r\x00payload", raw, sum[:])
	if err != nil || !ok || string(got) != string(canonical) {
		t.Fatalf("first verify = %s %v %v", got, ok, err)
	}
	if len(m.entries) != 1 {
		t.Fatal("verified result was not remembered")
	}
	tampered := []byte(`{"b": 1, "a": 3}`)
	if _, ok, err := m.canonicalVerified("r\x00payload", tampered, sum[:]); err != nil || ok {
		t.Fatalf("tampered payload passed against the stored hash: ok=%v err=%v", ok, err)
	}
	otherSum := sha256.Sum256([]byte("x"))
	if _, ok, _ := m.canonicalVerified("r\x00payload", raw, otherSum[:]); ok {
		t.Fatal("tampered hash column passed")
	}
	if _, _, err := m.canonicalVerified("r\x00payload", []byte(`{"a":1,"a":2}`), sum[:]); err == nil {
		t.Fatal("duplicate keys accepted")
	}
}
