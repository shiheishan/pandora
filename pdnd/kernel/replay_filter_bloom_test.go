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

// saltStream 生成互不相同的 32 字节 salt（前 24 字节随机、后 8 字节计数）。
type saltStream struct {
	key [32]byte
	n   uint64
}

func newSaltStream(t testing.TB) *saltStream {
	s := &saltStream{}
	copy(s.key[:24], randomSalt(t, 24))
	return s
}

func (s *saltStream) next() []byte {
	s.n++
	binary.LittleEndian.PutUint64(s.key[24:], s.n)
	return s.key[:]
}

// 保留期按时间：低流量下一个 salt 写入后至少记一代（15 分钟）、两代之后清掉。
func TestSaltBloomRetentionByTime(t *testing.T) {
	b := newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
	base := time.Unix(1_700_000_000, 0)
	key := randomSalt(t, 32)
	// 先跑 7 分钟背景流量，让 key 落在一代的中间。
	bg := newSaltStream(t)
	at := base
	for ; at.Before(base.Add(7 * time.Minute)); at = at.Add(100 * time.Millisecond) {
		b.check(bg.next(), at)
	}
	if !b.check(key, at) {
		t.Fatal("首次出现应放行")
	}
	written := at
	// 每秒 10 个连接，到写入后一整代（15 分钟）之前任何时刻重放都拦得住。
	for ; at.Before(written.Add(ssSaltGenPeriod - time.Second)); at = at.Add(100 * time.Millisecond) {
		b.check(bg.next(), at)
		if at.Sub(written)%time.Minute == 0 && b.check(key, at) {
			t.Fatalf("写入 %v 后的重放被放行", at.Sub(written))
		}
	}
	if b.check(key, at) {
		t.Fatal("一代之内的重放被放行")
	}
	// 两代之后清掉。
	if !b.check(key, written.Add(2*ssSaltGenPeriod+time.Second)) {
		t.Fatal("超过两代的键仍未清掉")
	}
}

// 旧实现（1 分钟 × 3 代）3 分钟后就忘；GFW 的重放一半晚于 1 分钟、75% 在 15 分钟
// 以内（IMC'20）。经生产构造的适配器，10 分钟后的重放仍拦得住。
func TestShadowsocksSaltReplayOutlivesOldWindow(t *testing.T) {
	value, err := newShadowsocksAdapter(InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Port: 8388, Raw: map[string]any{"method": "aes-128-gcm"}}})
	if err != nil {
		t.Fatal(err)
	}
	a := value.(*shadowsocksAdapter)
	salt := randomSalt(t, 16)
	now := time.Now()
	if !a.salts.check(salt, now) {
		t.Fatal("首次出现应放行")
	}
	if a.salts.check(salt, now.Add(10*time.Minute)) {
		t.Fatal("10 分钟后的重放被放行")
	}
}

// 被洪泛时内存封顶：当前块写满就提前换代、下一块翻倍，至多两块最大块；最近
// 写入的仍拦得住。
func TestSaltBloomFloodIsBounded(t *testing.T) {
	b := newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
	now := time.Unix(1_700_000_000, 0)
	s := newSaltStream(t)
	var last []byte
	for i := 0; i < 3*ssSaltBloomMaxCapacity; i++ {
		last = s.next()
		b.check(last, now)
	}
	maxBlock := newBloomBlock(ssSaltBloomMaxCapacity, ssSaltBloomFPRate).bytes()
	if got := b.residentBytes(); got > 2*maxBlock {
		t.Fatalf("位图 %d 字节，超过两块最大块 %d", got, 2*maxBlock)
	}
	if b.check(last, now) {
		t.Fatal("最近写入的键应仍被拦住")
	}
}

// 空闲两代后两块都丢掉：空闲进程不占位图。
func TestSaltBloomIdleReleasesBlocks(t *testing.T) {
	b := newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
	now := time.Unix(1_700_000_000, 0)
	b.check(randomSalt(t, 32), now)
	b.mu.Lock()
	b.expireLocked(now.Add(2 * ssSaltGenPeriod))
	b.mu.Unlock()
	if got := b.residentBytes(); got != 0 {
		t.Fatalf("空闲两代后仍占 %d 字节", got)
	}
}

// 常驻内存随流量走：每秒 10 / 100 个新连接持续 1 小时，以及洪泛（同一时刻 300 万
// 条），与改前的精确表（1 分钟 × 3 代、每代至多 65536 条）比堆增量。低负载两档
// 不得高于改前，洪泛封顶 8MB。
func TestSaltBloomMemoryTracksLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	measure := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}
	type load struct {
		name     string
		perSec   int
		duration time.Duration
	}
	run := func(l load, check func(key []byte, now time.Time) bool) int64 {
		before := measure()
		start := time.Unix(1_700_000_000, 0)
		s := newSaltStream(t)
		total := int(l.duration/time.Second) * l.perSec
		step := time.Second / time.Duration(l.perSec)
		for i := 0; i < total; i++ {
			check(s.next(), start.Add(time.Duration(i)*step))
		}
		return int64(measure()) - int64(before)
	}
	for _, l := range []load{{"每秒 10", 10, time.Hour}, {"每秒 100", 100, time.Hour}, {"每秒 1000", 1000, 20 * time.Minute}} {
		bloom := newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
		newBytes := run(l, bloom.check)
		legacy := newReplayFilter(time.Minute, 3, replayFilterMaxPerGen)
		oldBytes := run(l, legacy.check)
		runtime.KeepAlive(bloom)
		runtime.KeepAlive(legacy)
		t.Logf("%s：改前 %.3f MB（保留 2–3 分钟），改后 %.3f MB（位图 %.3f MB，保留 15–30 分钟）",
			l.name, float64(oldBytes)/(1<<20), float64(newBytes)/(1<<20), float64(bloom.residentBytes())/(1<<20))
		if newBytes > oldBytes || int64(bloom.residentBytes()) > oldBytes {
			t.Errorf("%s：改后 %d 字节高于改前 %d", l.name, newBytes, oldBytes)
		}
	}
	before := measure()
	flood := newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
	now := time.Unix(1_700_000_000, 0)
	s := newSaltStream(t)
	for i := 0; i < 3*ssSaltBloomMaxCapacity; i++ {
		flood.check(s.next(), now)
	}
	floodBytes := int64(measure()) - int64(before)
	runtime.KeepAlive(flood)
	t.Logf("洪泛 300 万条：改后 %.2f MB（改前封顶约 16 MB）", float64(floodBytes)/(1<<20))
	if floodBytes > 8<<20 {
		t.Fatalf("洪泛堆增量 %d 字节超过 8MB", floodBytes)
	}
}

// 爬坡时的最短保留期（review-r3 #2）：刚启动、空闲两代被清空之后、流量突增时，
// 块从小往大长。以前写满即提前换代、丢掉上一块，每秒 100 个连接时只记得约 41
// 秒前的 salt，每秒 1000 个时约 4 秒；改前精确表（1 分钟 × 3 代、每代至多 65536
// 条）在同样负载下至少记 2 分钟（时间下限）或 131072 条（每秒 1000 个即 131 秒）。
// 从冷启动爬坡 20 分钟（跨一次换代）、空闲 31 分钟（两代清空）、再爬坡 5 分钟，
// 每个模拟秒都查 L 秒前写入的 salt 仍拦得住，L 取改前下限。
func TestSaltBloomRampRetentionNotBelowOld(t *testing.T) {
	for _, tc := range []struct {
		perSec int
		floor  time.Duration // 改前精确表在该负载下的最短保留期
	}{
		{100, 120 * time.Second},
		{1000, 131 * time.Second},
	} {
		b := newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
		s := newSaltStream(t)
		start := time.Unix(1_700_000_000, 0)
		maxBytes := 0
		ramp := func(from time.Time, d time.Duration) {
			seconds := int(d / time.Second)
			firsts := make([][]byte, seconds)
			step := time.Second / time.Duration(tc.perSec)
			for sec := 0; sec < seconds; sec++ {
				base := from.Add(time.Duration(sec) * time.Second)
				for i := 0; i < tc.perSec; i++ {
					key := s.next()
					if i == 0 {
						firsts[sec] = append([]byte(nil), key...)
					}
					if !b.check(key, base.Add(time.Duration(i)*step)) {
						continue // 误报：不影响保留期的判定
					}
				}
				if old := sec - int(tc.floor/time.Second); old >= 0 && !b.contains(firsts[old]) {
					t.Fatalf("每秒 %d：第 %d 秒时已忘掉 %v 前写入的 salt（改前下限 %v）",
						tc.perSec, int(base.Sub(start)/time.Second), tc.floor, tc.floor)
				}
				maxBytes = max(maxBytes, b.residentBytes())
			}
		}
		ramp(start, 20*time.Minute)
		ramp(start.Add(51*time.Minute), 5*time.Minute)
		t.Logf("每秒 %d：爬坡全程保留期 ≥ %v，位图峰值 %.2f MB", tc.perSec, tc.floor, float64(maxBytes)/(1<<20))
		if maxBytes > 2*newBloomBlock(ssSaltBloomMaxCapacity, ssSaltBloomFPRate).bytes() {
			t.Fatalf("位图峰值 %d 字节超过两块最大块", maxBytes)
		}
	}
}

// 误报率：两块都写满时，新键被当成重放的比例应在设计值量级（每块 1e-6）。
func TestSaltBloomFalsePositiveRate(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	const capacity = 200_000
	b := newSaltBloom(time.Hour, capacity, capacity, 1e-6)
	now := time.Unix(1_700_000_000, 0)
	s := newSaltStream(t)
	for i := 0; i < 2*capacity; i++ {
		b.check(s.next(), now)
	}
	falsePositives := 0
	const probes = 500_000
	for i := 0; i < probes; i++ {
		if b.contains(s.next()) {
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
	b := newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
	salt := randomSalt(t, 32)
	now := time.Now()
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.check(salt, now) {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != 1 {
		t.Fatalf("同一个 salt 放行了 %d 次", passed.Load())
	}
}

// 每个新连接一次防重放检查（新 salt：查不到、写入）的开销。
// go test ./kernel -run '^$' -bench 'SSSaltReplay' -benchmem
func BenchmarkSSSaltReplay(b *testing.B) {
	b.Run("bloom", func(b *testing.B) {
		f := newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
		s := newSaltStream(b)
		now := time.Now()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.check(s.next(), now)
		}
	})
	b.Run("legacy-generations", func(b *testing.B) {
		f := newReplayFilter(time.Minute, 3, replayFilterMaxPerGen)
		s := newSaltStream(b)
		now := time.Now()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.check(s.next(), now)
		}
	})
}
