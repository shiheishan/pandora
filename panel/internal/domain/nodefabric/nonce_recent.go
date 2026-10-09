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
// 值是 8 字节戳、到期顺序放环形队列：6.7 万条 5.51MB（82 字节一条），20 万条 11.13MB
// （TestRecentNonceSetMemory）。Go 的 map 不缩容，高峰过后表本身留在峰值大小。
//
// 键长只影响可用性、不影响防重放：同一请求永远算出同一个键，碰撞只会把另一条合法请求
// 误判成重放（401，节点换个 nonce 重试），绝不会把重放放过去。128 位截断的 SHA-256
// 对随机 nonce 的误判概率可以忽略，节点也预测不了别的节点下一个 nonce。

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

// recentStamp 是一条的到期时刻（Unix 纳秒，墙钟），最低位借作 unstored 标记：
// 置位表示 Valkey 还没确认持有这条（认领在途，或回落到了 PG）。
//
// 用墙钟而不用单调钟：签名时间戳的 ±5 分钟窗口也是按墙钟判的，两边同一把尺子，
// 墙钟跳变时「近期集忘掉一条」与「这条的签名时间戳出窗」同步发生。
type recentStamp int64

func newRecentStamp(expires time.Time) recentStamp {
	return recentStamp(expires.UnixNano()) | 1
}

func (s recentStamp) expiresNs() int64 { return int64(s) &^ 1 }
func (s recentStamp) unstored() bool   { return s&1 == 1 }
func (s recentStamp) stored() recentStamp {
	return s &^ 1
}

// recentSet 是近期集本体，另带 PG 补查界（谁的签名时间戳要在 PG 再认领一次）。
type recentSet struct {
	mu      sync.Mutex
	entries map[recentKey]recentStamp
	order   keyRing // 按认领先后，也就是按到期先后；与 entries 的键一一对应
	max     int
	// recheckNs：签名时间戳（Unix 纳秒）不晚于它的请求，Valkey 认领成功后还要在 PG
	// 补查一次。0 表示不用补查。只升不降；过了它 5 分钟，再没有请求能满足条件。
	recheckNs int64
}

func newRecentSet(max int) *recentSet {
	return &recentSet{entries: make(map[recentKey]recentStamp), max: max}
}

// claim 原子地「查并占」key。已在集里返回 false。新条目带 unstored 标记，直到 Valkey
// 确认持有（markStored）。
func (r *recentSet) claim(key recentKey, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	if _, ok := r.entries[key]; ok {
		return false
	}
	r.entries[key] = newRecentStamp(now.Add(signedNonceRetention))
	r.order.push(key)
	return true
}

// pruneLocked 删掉已过期的条目；到了上限再挤掉最早的，给新条目腾一个位置。
//
// 挤掉一条还没过期、Valkey 又没确认持有的条目时，它可能只记在 PG 里：把补查界抬到
// 「它的认领时刻 + 5 分钟」。这条 nonce 的签名时间戳不会晚于认领时刻 + 5 分钟（否则
// 当时就被时间窗口拒了），重放带的是同一个时间戳，所以一定落在补查范围里，撞 PG 主键。
func (r *recentSet) pruneLocked(now time.Time) {
	nowNs := now.UnixNano()
	for r.order.len() > 0 {
		key := r.order.front()
		stamp := r.entries[key]
		live := stamp.expiresNs() > nowNs
		if live && r.order.len() < r.max {
			return
		}
		if live && stamp.unstored() {
			claimedNs := stamp.expiresNs() - int64(signedNonceRetention)
			r.raiseRecheckLocked(claimedNs + int64(SignedRequestAcceptanceWindow))
		}
		delete(r.entries, key)
		r.order.pop()
	}
}

// markStored 记下 Valkey 已持有 key：之后被挤掉也不用补查 PG。
func (r *recentSet) markStored(key recentKey) {
	r.mu.Lock()
	if stamp, ok := r.entries[key]; ok {
		r.entries[key] = stamp.stored()
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
