package nodefabric

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"
)

// 进程内近期集：本进程认领过、还在保留期内的 nonce。它是 nonce 认领的第一道（原子的
// 「查并占」），也是 Valkey 与 PG 之间那道缝的主要补丁：回落期间只记在 PG 里的 nonce，
// 本进程的近期集同样记着，恢复后换到 Valkey 重放也会先在这里被拦下。
//
// 紧凑存储（2026-10 w12nonce）：原先是 map[string]time.Time 加 []recentNonce，键是
// 「租户:节点:nonce」字符串，实测 6.7 万条 15.59MB（233 字节一条）。现在键是 16 字节摘要、
// 值是两个 int64（单调钟到期、签名时间戳），到期顺序放环形队列：6.7 万条 7.26MB（108 字节
// 一条），20 万条 14.64MB（TestRecentNonceSetMemory）。两个值能硬塞进一个 int64（毫秒回绕
// 比较加整秒时间戳），每条再省约 25 字节，但回绕算术难维护，不做。
// Go 的 map 不缩容，高峰过后表本身留在峰值大小。
//
// 到期按单调钟算（进程内经过的时长），不按墙钟：墙钟先前跳再跳回，不会让条目提前过期、
// 在签名时间戳仍在窗口内时被忘掉（对抗审查 w12nonce #1、#6）。墙钟只出现在签名时间戳与
// 补查界的比较里，而那里比较的是请求自己带的、签了名的时间戳，与本机墙钟无关。
//
// 键长只影响可用性、基本不影响防重放：同一请求永远算出同一个键，碰撞通常只会把另一条
// 合法请求误判成重放（401，节点换个 nonce 重试）。唯一的理论例外：A 的 Valkey 认领在途时
// A 被挤出近期集，与 A 碰撞的 B 以同一个键进来并回落，A 的 markStored 再清掉 B 的
// unstored 位，B 之后被挤掉时不抬补查界。这要 128 位碰撞外加 250ms 内挤满整个近期集，
// 可以忽略。128 位截断的 SHA-256 对随机 nonce 的误判概率同样可以忽略。

// recentKey 是近期集里一条 nonce 的键：sha256(种类‖租户‖身份‖nonce) 的前 16 字节。
type recentKey [16]byte

const (
	recentKindNode   byte = 'n'
	recentKindServer byte = 's'
)

// makeRecentKey 算近期集的键。租户与身份带长度前缀，种类字节把节点与服务器两个键空间分开。
func makeRecentKey(kind byte, tenantID, subjectID string, nonce []byte) recentKey {
	var buf [128]byte
	b := append(buf[:0], kind)
	b = binary.AppendUvarint(b, uint64(len(tenantID)))
	b = append(b, tenantID...)
	b = binary.AppendUvarint(b, uint64(len(subjectID)))
	b = append(b, subjectID...)
	b = append(b, nonce...)
	sum := sha256.Sum256(b)
	var k recentKey
	copy(k[:], sum[:len(k)])
	return k
}

// recentEntry 是近期集的一条。
//
// expires 是到期时刻（单调钟，相对 recentSet 所属守卫的起点，纳秒），最低位借作 unstored
// 标记：置位表示 Valkey 还没确认持有这条（认领在途，或回落到了 PG）。
// ts 是这条请求签了名的时间戳（Unix 纳秒）：重放带的是同一个时间戳，unstored 的条目离开
// 近期集时把补查界抬到它，重放就一定会在 PG 补查、撞主键。
type recentEntry struct {
	expires int64
	ts      int64
}

func (e recentEntry) expiresAt() time.Duration { return time.Duration(e.expires &^ 1) }
func (e recentEntry) unstored() bool           { return e.expires&1 == 1 }

// recentSet 是近期集本体，另带 PG 补查界（谁的签名时间戳要在 PG 再认领一次）。
type recentSet struct {
	mu      sync.Mutex
	entries map[recentKey]recentEntry
	order   keyRing // 按认领先后，也就是按到期先后（单调钟）；与 entries 的键一一对应
	max     int
	// recheckNs：签名时间戳（Unix 纳秒）不晚于它的请求，Valkey 认领成功后还要在 PG
	// 补查一次。0 表示不用补查。只升不降。
	recheckNs int64
}

func newRecentSet(max int) *recentSet {
	return &recentSet{entries: make(map[recentKey]recentEntry), max: max}
}

// claim 原子地「查并占」key。已在集里返回 false。now 是单调钟读数，ts 是请求签了名的
// 时间戳。新条目带 unstored 标记，直到 Valkey 确认持有（markStored）。
func (r *recentSet) claim(key recentKey, now time.Duration, ts time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	if _, ok := r.entries[key]; ok {
		return false
	}
	r.entries[key] = recentEntry{expires: int64(now+signedNonceRetention) | 1, ts: ts.UnixNano()}
	r.order.push(key)
	return true
}

// pruneLocked 删掉已过期的条目；到了上限再挤掉最早的，给新条目腾一个位置。
//
// 删掉（到期或被挤掉）一条 Valkey 没确认持有的条目时，它可能只记在 PG 里：把补查界抬到
// 它的签名时间戳。重放带的是同一个时间戳，一定落在补查范围里，撞 PG 主键。到期的条目
// 平时早已出了时间戳窗口，抬界不会让任何合法请求多补查；墙钟往回跳、旧时间戳重新
// 进窗口时，这一步让重放仍去 PG 补查。
func (r *recentSet) pruneLocked(now time.Duration) {
	for r.order.len() > 0 {
		key := r.order.front()
		e := r.entries[key]
		if e.expiresAt() > now && r.order.len() < r.max {
			return
		}
		if e.unstored() {
			r.raiseRecheckLocked(e.ts)
		}
		delete(r.entries, key)
		r.order.pop()
	}
}

// markStored 记下 Valkey 已持有 key：之后离开近期集也不用补查 PG。
func (r *recentSet) markStored(key recentKey) {
	r.mu.Lock()
	if e, ok := r.entries[key]; ok {
		e.expires &^= 1
		r.entries[key] = e
	}
	r.mu.Unlock()
}

// needsRecheck 判断签名时间戳为 ts 的请求在 Valkey 认领成功后是否还要在 PG 补查。
func (r *recentSet) needsRecheck(ts time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recheckNs != 0 && ts.UnixNano() <= r.recheckNs
}

func (r *recentSet) raiseRecheck(ts time.Time) {
	r.mu.Lock()
	r.raiseRecheckLocked(ts.UnixNano())
	r.mu.Unlock()
}

func (r *recentSet) raiseRecheckLocked(ns int64) {
	if ns > r.recheckNs {
		r.recheckNs = ns
	}
}

func (r *recentSet) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.order.len()
}

// keyRing 是按到期先后排的键队列：容量取 2 的幂，满了翻倍，用量跌到四分之一就减半，
// 一阵高峰过后不一直占着峰值的内存。
type keyRing struct {
	buf  []recentKey
	head int
	n    int
}

const keyRingMinCap = 1024

func (q *keyRing) len() int { return q.n }

func (q *keyRing) front() recentKey { return q.buf[q.head] }

func (q *keyRing) push(k recentKey) {
	if q.n == len(q.buf) {
		q.resize(max(keyRingMinCap, 2*len(q.buf)))
	}
	q.buf[(q.head+q.n)&(len(q.buf)-1)] = k
	q.n++
}

func (q *keyRing) pop() {
	q.buf[q.head] = recentKey{}
	q.head = (q.head + 1) & (len(q.buf) - 1)
	q.n--
	if len(q.buf) > keyRingMinCap && q.n < len(q.buf)/4 {
		q.resize(len(q.buf) / 2)
	}
}

func (q *keyRing) resize(size int) {
	next := make([]recentKey, size)
	for i := 0; i < q.n; i++ {
		next[i] = q.buf[(q.head+i)&(len(q.buf)-1)]
	}
	q.buf, q.head = next, 0
}
