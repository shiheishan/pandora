package kernel

import (
	"hash/maphash"
	"math"
	"sync"
)

// saltBloom 是旧版 Shadowsocks AEAD 的 TCP salt 防重放表：两块 Bloom 位图轮换
// （ping-pong，shadowsocks-libev 的 ppbloom 同一思路）。
//
// 为什么不用按时间分代的 replayFilter：旧版 AEAD 没有时间戳，只能靠「记多久」
// 防重放。GFW 的重放探测一半晚于 1 分钟、最长约 570 小时（IMC'20），按时间分代
// 想记到小时级，内存就与连接速率成正比、没有上限。这里改成按条数保留：
//   - 写入只进当前块；当前块写满 capacity 条时清空另一块并互换，保留的就是
//     最近 capacity 到 2*capacity 条，与经过多久无关（每秒 10 个新连接约记
//     28 到 56 小时）；
//   - 内存至多两块位图（生产参数每块约 3.6MB），不随流量增长。
//
// 误报的后果是极少数合法连接被当成重放拒掉（每块 1e-6）。SIP022 禁止 Bloom
// 只针对 SS2022（它另有时间戳与精确表），不约束旧版 AEAD。
//
// 只记录认证通过的 salt（调用方保证）：没有口令的人写不进这张表，也就无法
// 把它灌满、缩短别人的保留期。
type saltBloom struct {
	mu       sync.Mutex
	seed     maphash.Seed
	capacity int
	k        uint32
	mBits    uint64
	// cur 是正在写入的块，prev 是上一轮写满的块。都按需分配：cur 在第一次
	// 写入时，prev 在第一次轮换时（每秒 10 个新连接要一天多才轮换一次），没有
	// 旧版 SS 流量的进程一个字节也不占。
	cur, prev []uint64
	count     int
}

const (
	// ssSaltBloomCapacity 是每块的设计条数，ssSaltBloomFPRate 是每块写满时的误报率。
	ssSaltBloomCapacity = 1_000_000
	ssSaltBloomFPRate   = 1e-6
)

// sharedSSSaltBloom 是全进程共用的一张表：每个旧版 SS 入站各开一张会让常驻内存
// 随入站数线性增长；salt 是 16–32 字节随机数，不同入站之间不会撞。入站重建
// （换配置）也不会清掉已记的 salt。
var sharedSSSaltBloom = sync.OnceValue(func() *saltBloom {
	return newSaltBloom(ssSaltBloomCapacity, ssSaltBloomFPRate)
})

// newSaltBloom 按每块 capacity 条、写满时误报率 fpRate 取位数与哈希个数
// （m = -n·ln p / ln²2，k = m/n·ln2）。
func newSaltBloom(capacity int, fpRate float64) *saltBloom {
	if capacity < 1 {
		capacity = 1
	}
	m := math.Ceil(-float64(capacity) * math.Log(fpRate) / (math.Ln2 * math.Ln2))
	words := uint64(m+63) / 64
	k := uint32(math.Round(float64(words*64) / float64(capacity) * math.Ln2))
	if k < 1 {
		k = 1
	}
	return &saltBloom{seed: maphash.MakeSeed(), capacity: capacity, k: k, mBits: words * 64}
}

// check 报告 key 是否没见过；没见过就记下并返回 true，见过（或误报）返回 false。
// 查与写在同一把锁里：同一个 salt 并发到达也只放行一次。
func (b *saltBloom) check(key []byte) bool {
	h1, h2 := b.hashes(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur == nil {
		b.cur = make([]uint64, b.mBits/64)
	}
	if b.testLocked(b.cur, h1, h2) || b.testLocked(b.prev, h1, h2) {
		return false
	}
	if b.count >= b.capacity {
		if b.prev == nil {
			b.prev = make([]uint64, b.mBits/64)
		}
		b.cur, b.prev = b.prev, b.cur
		clear(b.cur)
		b.count = 0
	}
	for i := uint32(0); i < b.k; i++ {
		bit := b.bit(h1, h2, i)
		b.cur[bit/64] |= 1 << (bit % 64)
	}
	b.count++
	return true
}

// contains 只查不写（测试量误报用）。
func (b *saltBloom) contains(key []byte) bool {
	h1, h2 := b.hashes(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.testLocked(b.cur, h1, h2) || b.testLocked(b.prev, h1, h2)
}

// hashes 取两个 32 位哈希做双重哈希（Kirsch–Mitzenmacher）：第 i 个位置是
// h1 + i·h2。种子按进程随机，外人无法挑选落在同一组位上的 salt。
func (b *saltBloom) hashes(key []byte) (uint32, uint32) {
	h := maphash.Bytes(b.seed, key)
	return uint32(h), uint32(h>>32) | 1
}

// bit 把 32 位的 h1 + i·h2 按乘法缩放映射到 [0, mBits)，不用取模。
func (b *saltBloom) bit(h1, h2, i uint32) uint64 {
	return uint64(h1+i*h2) * b.mBits >> 32
}

func (b *saltBloom) testLocked(bits []uint64, h1, h2 uint32) bool {
	if bits == nil {
		return false
	}
	for i := uint32(0); i < b.k; i++ {
		bit := b.bit(h1, h2, i)
		if bits[bit/64]&(1<<(bit%64)) == 0 {
			return false
		}
	}
	return true
}
