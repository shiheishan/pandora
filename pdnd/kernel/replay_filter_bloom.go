package kernel

import (
	"hash/maphash"
	"math"
	"sync"
	"time"
)

// saltBloom 是旧版 Shadowsocks AEAD 的 TCP salt 防重放表：按时间分代的 Bloom
// 位图链。一代内按写入先后串若干块，块大小跟着流量长。
//
// 为什么不用按时间分代的精确表（replayFilter）：旧版 AEAD 没有时间戳，只能靠
// 「记多久」防重放。GFW 的重放探测一半晚于 1 分钟、75% 在 15 分钟内、最长约
// 570 小时（IMC'20）。精确表每条约 60 字节，以前只记 2–3 分钟；Bloom 每条约
// 3.6 字节（误报 1e-6），同样的内存能记十几倍久。
//
// 规则：
//   - 每 ssSaltGenPeriod 换一代：上上代的块整代丢掉，当前代变上一代。一个 salt
//     从写入起至少记一代、至多两代（15–30 分钟）——只要总内存没到上界，与流量
//     形状无关；
//   - 当前代的第一块按刚结束那一代的条数加 25% 余量定大小（下限
//     ssSaltBloomMinCapacity），常驻内存跟着实际流量走（TestSaltBloomMemoryTracksLoad）；
//   - 当前块写满就在本代内接一块翻倍的新块（上限 ssSaltBloomMaxCapacity），已写满
//     的块留着：刚启动、空闲之后或流量突增时块从小往大长，长的过程中不丢任何
//     salt（以前写满即提前换代、丢掉上一块，爬坡时只记得几秒前的，review-r3 #2）；
//   - 总容量超过两块最大块时才从最老的块起按块丢：被洪泛时内存封顶在两块最大块，
//     此时仍至少记着最近一整块最大块（约 100 万条，改前精确表最多 13 万条）；
//   - 整整两代没有新写入时全部丢掉，空闲进程不占位图。
//
// 误报的后果是极少数合法连接被当成重放拒掉：每块写满时 1e-6，查的是链上全部
// 块，稳态每代一两块；爬坡时链最长约 log2(最大块/最小块)+1 块，误报随之叠到十几
// 个 1e-6，只持续到下一次换代。SIP022 禁止 Bloom 只针对 SS2022（它另有时间戳与
// 精确表），不约束旧版 AEAD。
//
// 只记录认证通过的 salt（调用方保证）：没有口令的人写不进这张表，也就无法
// 把它灌满、缩短别人的保留期。
type saltBloom struct {
	mu     sync.Mutex
	seed   maphash.Seed
	period time.Duration
	minCap int
	maxCap int
	fpRate float64
	// blocks 从老到新；最后一块是当前写入块。每块记着它属于哪一代（gen 是该代的
	// 开始时刻），换代时整代丢掉上上代。
	blocks     []*bloomBlock
	genStarted time.Time // 当前代的开始时刻
	genCount   int       // 当前代已写入的条数
	nextCap    int       // 当前代第一块的大小（换代时按上一代条数定）
	capacity   int       // blocks 的容量合计，不超过 2*maxCap
}

const (
	// ssSaltGenPeriod 是一代的时长：保留期 15–30 分钟。取值让各档流量下的常驻
	// 内存不高于以前的精确表（以前每秒 r 个连接约 r×12KB，这里约 r×8KB）。
	ssSaltGenPeriod = 15 * time.Minute
	// ssSaltBloomMinCapacity 是块的最小条数（约 3.6KB）。
	ssSaltBloomMinCapacity = 1 << 10
	// ssSaltBloomMaxCapacity 是块的最大条数（约 3.6MB），总容量封顶两块即约 7.2MB。
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
	if b.testLocked(h1, h2) {
		return false
	}
	if len(b.blocks) == 0 {
		b.genStarted, b.genCount, b.nextCap = now, 0, b.minCap
	}
	cur := b.currentLocked()
	if cur == nil || cur.count >= cur.capacity {
		next := b.nextCap
		if cur != nil {
			// 本代内写满：接一块翻倍的。
			next = 2 * cur.capacity
		}
		cur = b.appendLocked(max(b.minCap, min(next, b.maxCap)))
	}
	cur.add(h1, h2)
	b.genCount++
	return true
}

// currentLocked 是当前代的写入块；当前代还没有块时返回 nil。
func (b *saltBloom) currentLocked() *bloomBlock {
	if n := len(b.blocks); n > 0 && b.blocks[n-1].gen.Equal(b.genStarted) {
		return b.blocks[n-1]
	}
	return nil
}

// appendLocked 在链尾接一块 capacity 条的新块；总容量会超过两块最大块时，先从
// 最老的块起丢。
func (b *saltBloom) appendLocked(capacity int) *bloomBlock {
	for len(b.blocks) > 0 && b.capacity+capacity > 2*b.maxCap {
		b.capacity -= b.blocks[0].capacity
		b.blocks[0] = nil
		b.blocks = b.blocks[1:]
	}
	block := newBloomBlock(capacity, b.fpRate)
	block.gen = b.genStarted
	b.blocks = append(b.blocks, block)
	b.capacity += capacity
	return block
}

// expireLocked 按时间换代；整整两代没有新写入（也没有查询推动换代）时全部丢掉。
func (b *saltBloom) expireLocked(now time.Time) {
	if len(b.blocks) == 0 {
		return
	}
	elapsed := now.Sub(b.genStarted)
	switch {
	case elapsed >= 2*b.period:
		b.blocks, b.capacity = nil, 0
	case elapsed >= b.period:
		// 当前代变上一代，上上代整代丢掉；新一代第一块按刚结束这一代的条数定。
		prev := b.genStarted
		kept := b.blocks[:0]
		capacity := 0
		for _, block := range b.blocks {
			if block.gen.Equal(prev) {
				kept = append(kept, block)
				capacity += block.capacity
			}
		}
		clear(b.blocks[len(kept):])
		b.blocks, b.capacity = kept, capacity
		b.nextCap = b.genCount + b.genCount/4
		b.genStarted, b.genCount = prev.Add(b.period), 0
	}
}

func (b *saltBloom) testLocked(h1, h2 uint32) bool {
	for i := len(b.blocks) - 1; i >= 0; i-- {
		if b.blocks[i].test(h1, h2) {
			return true
		}
	}
	return false
}

// contains 只查不写（测试量误报与保留期用）。
func (b *saltBloom) contains(key []byte) bool {
	h1, h2 := b.hashes(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.testLocked(h1, h2)
}

// residentBytes 是链上全部位图的字节数（测试与诊断用）。
func (b *saltBloom) residentBytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := 0
	for _, block := range b.blocks {
		total += block.bytes()
	}
	return total
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
	gen      time.Time // 所属代的开始时刻
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
