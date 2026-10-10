package kernel

import (
	"hash/maphash"
	"math"
	"sync"
	"time"
)

// saltBloom 是旧版 Shadowsocks AEAD 的 TCP salt 防重放表：按时间分代的两块
// Bloom 位图（当前代 cur、上一代 prev），每代的块大小按上一代的实际条数定。
//
// 为什么不用按时间分代的精确表（replayFilter）：旧版 AEAD 没有时间戳，只能靠
// 「记多久」防重放。GFW 的重放探测一半晚于 1 分钟、75% 在 15 分钟内、最长约
// 570 小时（IMC'20）。精确表每条约 60 字节，以前只记 2–3 分钟；Bloom 每条约
// 3.6 字节（误报 1e-6），同样的内存能记十几倍久。
//
// 规则：
//   - 每 ssSaltGenPeriod 换一代：prev 丢掉，cur 变 prev，新开一块。一个 salt 从
//     写入起至少记一代、至多两代（15–30 分钟），与流量无关；
//   - 新块按刚结束那一代的条数加 25% 余量定大小（下限 ssSaltBloomMinCapacity）：
//     常驻内存跟着实际流量走，低负载不超过以前的精确表（实测见
//     TestSaltBloomMemoryTracksLoad）；
//   - 当前块写满就提前换代、下一块翻倍（上限 ssSaltBloomMaxCapacity）：流量突增
//     时块跟着长，被洪泛时内存封顶在两块最大块，保留期退化为「最近至少一块的
//     条数」，不会无限增长；
//   - 整整两代没有新写入时两块都丢掉，空闲进程不占位图。
//
// 误报的后果是极少数合法连接被当成重放拒掉（每块写满时 1e-6）。SIP022 禁止
// Bloom 只针对 SS2022（它另有时间戳与精确表），不约束旧版 AEAD。
//
// 只记录认证通过的 salt（调用方保证）：没有口令的人写不进这张表，也就无法
// 把它灌满、缩短别人的保留期。
type saltBloom struct {
	mu         sync.Mutex
	seed       maphash.Seed
	period     time.Duration
	minCap     int
	maxCap     int
	fpRate     float64
	cur, prev  *bloomBlock
	curStarted time.Time
}

const (
	// ssSaltGenPeriod 是一代的时长：保留期 15–30 分钟。取值让各档流量下的常驻
	// 内存不高于以前的精确表（以前每秒 r 个连接约 r×12KB，这里约 r×8KB）。
	ssSaltGenPeriod = 15 * time.Minute
	// ssSaltBloomMinCapacity 是块的最小条数（约 3.6KB）。
	ssSaltBloomMinCapacity = 1 << 10
	// ssSaltBloomMaxCapacity 是块的最大条数（约 3.6MB），洪泛时两块封顶约 7.2MB。
	ssSaltBloomMaxCapacity = 1 << 20
	// ssSaltBloomFPRate 是每块写满时的误报率。
	ssSaltBloomFPRate = 1e-6
)

// sharedSSSaltBloom 是全进程共用的一张表：每个旧版 SS 入站各开一张会让常驻内存
// 随入站数线性增长；salt 是 16–32 字节随机数，不同入站之间不会撞。入站重建
// （换配置）也不会清掉已记的 salt。
var sharedSSSaltBloom = sync.OnceValue(func() *saltBloom {
	return newSaltBloom(ssSaltGenPeriod, ssSaltBloomMinCapacity, ssSaltBloomMaxCapacity, ssSaltBloomFPRate)
})

func newSaltBloom(period time.Duration, minCap, maxCap int, fpRate float64) *saltBloom {
	if minCap < 1 {
		minCap = 1
	}
	if maxCap < minCap {
		maxCap = minCap
	}
	return &saltBloom{seed: maphash.MakeSeed(), period: period, minCap: minCap, maxCap: maxCap, fpRate: fpRate}
}

// check 报告 key 是否没见过；没见过就记下并返回 true，见过（或误报）返回 false。
// 查与写在同一把锁里：同一个 salt 并发到达也只放行一次。
func (b *saltBloom) check(key []byte, now time.Time) bool {
	h1, h2 := b.hashes(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked(now)
	if b.cur.test(h1, h2) || b.prev.test(h1, h2) {
		return false
	}
	switch {
	case b.cur == nil:
		b.cur, b.curStarted = newBloomBlock(b.minCap, b.fpRate), now
	case b.cur.count >= b.cur.capacity:
		// 一代没过完就写满：提前换代，下一块翻倍。
		b.rotateLocked(min(2*b.cur.capacity, b.maxCap), now)
	}
	b.cur.add(h1, h2)
	return true
}

// expireLocked 按时间换代；整整两代没有新写入时两块都丢掉。
func (b *saltBloom) expireLocked(now time.Time) {
	if b.cur == nil {
		return
	}
	elapsed := now.Sub(b.curStarted)
	switch {
	case elapsed >= 2*b.period:
		b.cur, b.prev = nil, nil
	case elapsed >= b.period:
		b.rotateLocked(b.cur.count+b.cur.count/4, b.curStarted.Add(b.period))
	}
}

// rotateLocked 丢掉 prev，cur 变 prev，按 capacity 新开当前块（夹在上下限之间）。
func (b *saltBloom) rotateLocked(capacity int, started time.Time) {
	capacity = max(b.minCap, min(capacity, b.maxCap))
	b.prev = b.cur
	b.cur, b.curStarted = newBloomBlock(capacity, b.fpRate), started
}

// contains 只查不写（测试量误报用）。
func (b *saltBloom) contains(key []byte) bool {
	h1, h2 := b.hashes(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cur.test(h1, h2) || b.prev.test(h1, h2)
}

// residentBytes 是两块位图的字节数（测试与诊断用）。
func (b *saltBloom) residentBytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cur.bytes() + b.prev.bytes()
}

// hashes 取两个 32 位哈希做双重哈希（Kirsch–Mitzenmacher）：第 i 个位置是
// h1 + i·h2。种子按进程随机，外人无法挑选落在同一组位上的 salt。
func (b *saltBloom) hashes(key []byte) (uint32, uint32) {
	h := maphash.Bytes(b.seed, key)
	return uint32(h), uint32(h>>32) | 1
}

// bloomBlock 是一块按 capacity 条、写满时误报率 fpRate 定尺寸的 Bloom 位图
// （m = -n·ln p / ln²2，k = m/n·ln2）。
type bloomBlock struct {
	bits     []uint64
	mBits    uint64
	k        uint32
	capacity int
	count    int
}

func newBloomBlock(capacity int, fpRate float64) *bloomBlock {
	m := math.Ceil(-float64(capacity) * math.Log(fpRate) / (math.Ln2 * math.Ln2))
	words := uint64(m+63) / 64
	k := uint32(math.Round(float64(words*64) / float64(capacity) * math.Ln2))
	if k < 1 {
		k = 1
	}
	return &bloomBlock{bits: make([]uint64, words), mBits: words * 64, k: k, capacity: capacity}
}

// bit 把 32 位的 h1 + i·h2 按乘法缩放映射到 [0, mBits)，不用取模。
func (b *bloomBlock) bit(h1, h2, i uint32) uint64 {
	return uint64(h1+i*h2) * b.mBits >> 32
}

func (b *bloomBlock) test(h1, h2 uint32) bool {
	if b == nil {
		return false
	}
	for i := uint32(0); i < b.k; i++ {
		bit := b.bit(h1, h2, i)
		if b.bits[bit/64]&(1<<(bit%64)) == 0 {
			return false
		}
	}
	return true
}

func (b *bloomBlock) add(h1, h2 uint32) {
	for i := uint32(0); i < b.k; i++ {
		bit := b.bit(h1, h2, i)
		b.bits[bit/64] |= 1 << (bit % 64)
	}
	b.count++
}

func (b *bloomBlock) bytes() int {
	if b == nil {
		return 0
	}
	return len(b.bits) * 8
}
