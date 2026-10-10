package kernel

import (
	"crypto/rand"
	"encoding/binary"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

func randomSalt(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// 保留期按条数而不是按时间：一个 salt 写入后，至少再写 capacity-1 条、
// 至多再写 2*capacity-1 条之前都拦得住；与经过多久无关。
func TestSaltBloomRetentionByCount(t *testing.T) {
	const capacity = 1000
	b := newSaltBloom(capacity, 1e-6)
	key := randomSalt(t, 32)
	if !b.check(key) {
		t.Fatal("首次出现应放行")
	}
	var other [32]byte
	for i := 1; i < 2*capacity; i++ {
		binary.BigEndian.PutUint64(other[:], uint64(i))
		if !b.check(other[:]) {
			t.Fatalf("第 %d 个新键被当成重放（误报）", i)
		}
		// 抽查：两轮之内任何时刻重放都要拦住。
		if i%97 == 0 && b.check(key) {
			t.Fatalf("写入 %d 条之后的重放被放行", i)
		}
	}
	if b.check(key) {
		t.Fatal("2*capacity-1 条之内的重放被放行")
	}
	// 再写满一轮，最早那一半被整块清掉，同一个键重新放行。
	for i := 2 * capacity; i < 3*capacity; i++ {
		binary.BigEndian.PutUint64(other[:], uint64(i))
		b.check(other[:])
	}
	if !b.check(key) {
		t.Fatal("超过两轮的键仍未清掉")
	}
}

// 旧实现（1 分钟 × 3 代、每代至多 65536 条）在 3 分钟后、或洪泛下写满约 20 万
// 条后就忘掉 salt；GFW 的重放一半晚于 1 分钟、最长约 570 小时（IMC'20）。新实现
// 与时间无关、按条数保留：经生产构造的适配器在其后 25 万次认证之后仍拦得住。
func TestShadowsocksSaltReplayRetainedByCount(t *testing.T) {
	value, err := newShadowsocksAdapter(InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Port: 8388, Raw: map[string]any{"method": "aes-128-gcm"}}})
	if err != nil {
		t.Fatal(err)
	}
	a := value.(*shadowsocksAdapter)
	salt := randomSalt(t, 16)
	if !a.acceptSalt(salt) {
		t.Fatal("首次出现应放行")
	}
	var other [16]byte
	_, _ = rand.Read(other[:8])
	for i := 0; i < 250_000; i++ {
		binary.BigEndian.PutUint64(other[8:], uint64(i))
		a.acceptSalt(other[:])
	}
	if a.acceptSalt(salt) {
		t.Fatal("25 万次认证之后的重放被放行")
	}
}

// 误报率：两块都写满 capacity 条时，新键被当成重放的比例应在设计值量级
// （每块 1e-6，两块合计不超过 2e-6）。抽 50 万个新键，允许到 20 个。
func TestSaltBloomFalsePositiveRate(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	const capacity = 200_000
	b := newSaltBloom(capacity, 1e-6)
	var key [16]byte
	seq := uint64(0)
	next := func() []byte {
		seq++
		binary.LittleEndian.PutUint64(key[:8], seq)
		binary.LittleEndian.PutUint64(key[8:], seq*0x9e3779b97f4a7c15)
		return key[:]
	}
	for i := 0; i < 2*capacity-1; i++ {
		b.check(next())
	}
	falsePositives := 0
	const probes = 500_000
	for i := 0; i < probes; i++ {
		k := next()
		// 只查不写：用 contains 量误报，写入会推动轮换。
		if b.contains(k) {
			falsePositives++
		}
	}
	t.Logf("误报 %d / %d", falsePositives, probes)
	if falsePositives > 20 {
		t.Fatalf("误报 %d / %d，远超设计值", falsePositives, probes)
	}
}

// 并发下同一个 salt 只放行一次（查与写在同一把锁里）。
func TestSaltBloomConcurrentSameSalt(t *testing.T) {
	b := newSaltBloom(1000, 1e-6)
	salt := randomSalt(t, 32)
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.check(salt) {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != 1 {
		t.Fatalf("同一个 salt 放行了 %d 次", passed.Load())
	}
}

// 常驻内存有上限：按生产参数写满两轮，堆增量不超过两块位图（约 7.2MB）
// 加少量余量；与写入条数无关。
func TestSaltBloomMemoryIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	measure := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}
	before := measure()
	b := newSaltBloom(ssSaltBloomCapacity, ssSaltBloomFPRate)
	var key [32]byte
	fill := func(from, to int) int64 {
		for i := from; i < to; i++ {
			binary.LittleEndian.PutUint64(key[:], uint64(i))
			b.check(key[:])
		}
		return int64(measure()) - int64(before)
	}
	one := fill(0, ssSaltBloomCapacity)
	t.Logf("第一轮写满 %d 条：堆增量 %.2f MB（一块位图 %d 字）", ssSaltBloomCapacity, float64(one)/(1<<20), len(b.cur))
	all := fill(ssSaltBloomCapacity, 3*ssSaltBloomCapacity)
	runtime.KeepAlive(b)
	t.Logf("写入 %d 条后：堆增量 %.2f MB（两块）", 3*ssSaltBloomCapacity, float64(all)/(1<<20))
	if one > 4<<20 || all > 8<<20 {
		t.Fatalf("堆增量 %d / %d 字节，超过一块 4MB / 两块 8MB", one, all)
	}
}

// 每个新连接一次防重放检查（新 salt：查不到、写入）的开销。
// go test ./kernel -run '^$' -bench 'SSSaltReplay' -benchmem
func BenchmarkSSSaltReplay(b *testing.B) {
	fresh := func(key *[32]byte, i int) []byte {
		binary.LittleEndian.PutUint64(key[24:], uint64(i))
		return key[:]
	}
	b.Run("bloom", func(b *testing.B) {
		f := newSaltBloom(ssSaltBloomCapacity, ssSaltBloomFPRate)
		var key [32]byte
		_, _ = rand.Read(key[:24])
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.check(fresh(&key, i))
		}
	})
	b.Run("legacy-generations", func(b *testing.B) {
		f := newReplayFilter(time.Minute, 3, replayFilterMaxPerGen)
		var key [32]byte
		_, _ = rand.Read(key[:24])
		now := time.Now()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.check(fresh(&key, i), now)
		}
	})
}
