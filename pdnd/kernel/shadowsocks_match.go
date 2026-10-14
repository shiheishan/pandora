package kernel

import (
	"cmp"
	"crypto/cipher"
	"crypto/sha1"
	"encoding"
	"hash"
	"hash/maphash"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"
)

// Shadowsocks（经典 AEAD）多用户认证没有用户标识，只能拿首个长度块逐个用户
// 试解密。这里把每次握手的固定成本压到最低：
//   - 用户集合是原子发布的不可变快照，握手不加锁、不复制切片；
//   - 主密钥在 AddUsers / UpsertUsers 时已派生好（EVP_BytesToKey），握手只做
//     与 salt 相关的 HKDF-SHA1；salt 那一侧的 HMAC 填充块每次握手只算一次，
//     每个候选用户零分配；
//   - 按来源 IP 记住最近匹配到的几个用户，先试它们。它只是试解密的顺序提示，
//     每个候选（包括提示出来的）一律做完整的 AEAD 校验，绝不据此跳过认证；
//   - 来源 IP 没有提示时按快照顺序扫，快照按最近认证时间排序（活跃用户在前），
//     换 IP 的活跃用户不必陪跑整张表。排序同样只影响先后，不影响判定。

// ssUserSnapshot 是某一时刻用户集合的只读视图；发布后不再修改。
type ssUserSnapshot struct {
	list []*ssUser
	byID map[int64]*ssUser
	// sortedAt 是 list 按最近认证时间排序的时刻（UnixNano）。
	sortedAt int64
}

const (
	// ssReorderInterval：扫表命中位置靠后时，至多这么久重排一次快照。
	ssReorderInterval = 5 * time.Second
	// ssReorderDepth：命中位置不超过它就不值得重排。
	ssReorderDepth = 64
)

// publishUsersLocked 按 a.users 重建快照并原子发布；调用方持有 a.mu 写锁。
func (a *shadowsocksAdapter) publishUsersLocked() {
	snapshot := &ssUserSnapshot{list: make([]*ssUser, 0, len(a.users)), byID: make(map[int64]*ssUser, len(a.users)), sortedAt: time.Now().UnixNano()}
	for _, user := range a.users {
		snapshot.list = append(snapshot.list, user)
		if _, exists := snapshot.byID[user.ID]; !exists {
			snapshot.byID[user.ID] = user
		}
	}
	sortSSUsersByRecency(snapshot.list)
	a.snapshot.Store(snapshot)
}

// sortSSUsersByRecency 把最近认证过的用户排到前面。先抄下时间戳再排，
// 避免并发认证改写 lastAuth 让比较前后不一致。
func sortSSUsersByRecency(list []*ssUser) {
	type keyed struct {
		user *ssUser
		at   int64
	}
	keys := make([]keyed, len(list))
	for i, user := range list {
		keys[i] = keyed{user: user, at: user.lastAuth.Load()}
	}
	slices.SortStableFunc(keys, func(x, y keyed) int { return cmp.Compare(y.at, x.at) })
	for i := range keys {
		list[i] = keys[i].user
	}
}

// maybeReorderUsers 在扫表命中位置靠后且距上次排序超过间隔时，按最近认证时间
// 重排快照。用 CAS 发布：期间若用户集合已更新（新快照本身就是排好序的），
// 这次重排直接作废。同一时刻只有一个 goroutine 在排。
func (a *shadowsocksAdapter) maybeReorderUsers(snapshot *ssUserSnapshot, now int64) {
	if now-snapshot.sortedAt < int64(ssReorderInterval) || !a.reordering.CompareAndSwap(false, true) {
		return
	}
	defer a.reordering.Store(false)
	next := &ssUserSnapshot{list: slices.Clone(snapshot.list), byID: snapshot.byID, sortedAt: now}
	sortSSUsersByRecency(next.list)
	a.snapshot.CompareAndSwap(snapshot, next)
}

func (a *shadowsocksAdapter) loadUsers() *ssUserSnapshot {
	if snapshot := a.snapshot.Load(); snapshot != nil {
		return snapshot
	}
	return &ssUserSnapshot{}
}

// ssSubkeyInfo 是 SIP004 HKDF 的 info 参数。
var ssSubkeyInfo = []byte("ss-subkey")

// ssScratch 是一次认证内复用的暂存区，经 sync.Pool 回收，让逐用户试解密不分配。
type ssScratch struct {
	h         hash.Hash
	saltInner []byte // HMAC(salt, ·) 内层：(salt⊕ipad) 吃完后的 SHA1 状态
	saltOuter []byte // HMAC(salt, ·) 外层：(salt⊕opad) 吃完后的 SHA1 状态
	pad       [64]byte
	digest    [sha1.Size]byte
	prk       [sha1.Size]byte
	okm       [2 * sha1.Size]byte
	counter   [1]byte
	nonce     [12]byte
	plain     []byte
}

var ssScratchPool = sync.Pool{New: func() any {
	return &ssScratch{h: sha1.New(), saltInner: make([]byte, 0, 128), saltOuter: make([]byte, 0, 128), plain: make([]byte, 0, 64)}
}}

func getSSScratch() *ssScratch { return ssScratchPool.Get().(*ssScratch) }

func putSSScratch(sc *ssScratch) {
	// UDP 偶尔会把 plain 撑到 64KB，过大的不回池，免得池里长期攥着大块内存。
	if cap(sc.plain) > 2048 {
		sc.plain = make([]byte, 0, 64)
	}
	ssScratchPool.Put(sc)
}

// setSalt 预计算以 salt 为 HMAC 密钥的内外层填充状态（HKDF-Extract 的 salt 侧），
// 同一次握手里对所有候选用户共用。salt 最长 32 字节，不超过 SHA1 块长 64。
func (sc *ssScratch) setSalt(salt []byte) {
	sc.saltInner = sc.keyedState(sc.saltInner[:0], salt, 0x36)
	sc.saltOuter = sc.keyedState(sc.saltOuter[:0], salt, 0x5c)
}

func (sc *ssScratch) keyedState(dst, key []byte, pad byte) []byte {
	for i := range sc.pad {
		sc.pad[i] = pad
	}
	for i, b := range key {
		sc.pad[i] ^= b
	}
	sc.h.Reset()
	_, _ = sc.h.Write(sc.pad[:])
	state, err := sc.h.(encoding.BinaryAppender).AppendBinary(dst)
	if err != nil {
		// sha1 的序列化不会失败；真失败说明运行时换了实现，直接暴露。
		panic(err)
	}
	return state
}

func (sc *ssScratch) restore(state []byte) {
	if err := sc.h.(encoding.BinaryUnmarshaler).UnmarshalBinary(state); err != nil {
		panic(err)
	}
}

// subkey 计算 HKDF-SHA1(IKM=master, salt=setSalt 设定的 salt, info="ss-subkey")
// 的前 length 字节（length ≤ 40）。结果指向暂存区，下次调用前有效；与
// deriveSSSubkey（标准库 hkdf 参考实现）逐字节一致，见单测。
func (sc *ssScratch) subkey(master []byte, length int) []byte {
	// Extract：PRK = HMAC(salt, master)。
	sc.restore(sc.saltInner)
	_, _ = sc.h.Write(master)
	inner := sc.h.Sum(sc.digest[:0])
	sc.restore(sc.saltOuter)
	_, _ = sc.h.Write(inner)
	prk := sc.h.Sum(sc.prk[:0])
	// Expand：T(i) = HMAC(PRK, T(i-1) || info || i)。
	out := sc.okm[:0]
	var previous []byte
	for i := byte(1); len(out) < length; i++ {
		sc.counter[0] = i
		sc.hmacPRK(prk, 0x36)
		_, _ = sc.h.Write(previous)
		_, _ = sc.h.Write(ssSubkeyInfo)
		_, _ = sc.h.Write(sc.counter[:])
		inner = sc.h.Sum(sc.digest[:0])
		sc.hmacPRK(prk, 0x5c)
		_, _ = sc.h.Write(inner)
		start := len(out)
		out = sc.h.Sum(out)
		previous = out[start:]
	}
	return out[:length]
}

func (sc *ssScratch) hmacPRK(prk []byte, pad byte) {
	for i := range sc.pad {
		sc.pad[i] = pad
	}
	for i, b := range prk {
		sc.pad[i] ^= b
	}
	sc.h.Reset()
	_, _ = sc.h.Write(sc.pad[:])
}

// zeroNonce 返回全零 12 字节 nonce（SIP004 每个方向的第一个块）。
func (sc *ssScratch) zeroNonce() []byte {
	sc.nonce = [12]byte{}
	return sc.nonce[:]
}

// findSSUser 用 salt 对候选用户逐个试解密：先试来源 IP 提示的用户，再按快照
// 顺序试其余用户。try 拿到该用户的 AEAD 做完整校验，返回 true 即认证通过。
// 认证通过后把用户记进来源 IP 提示。
func (a *shadowsocksAdapter) findSSUser(sc *ssScratch, salt []byte, source netip.Addr, try func(cipher.AEAD) bool) (*ssUser, cipher.AEAD) {
	snapshot := a.loadUsers()
	if len(snapshot.list) == 0 {
		return nil, nil
	}
	sc.setSalt(salt)
	attempt := func(user *ssUser) cipher.AEAD {
		aead, err := a.method.NewAEAD(sc.subkey(user.MasterKey, a.method.KeyLen))
		if err != nil || !try(aead) {
			return nil
		}
		return aead
	}
	matched := func(user *ssUser) {
		now := time.Now().UnixNano()
		user.lastAuth.Store(now)
		a.hints.remember(source, user)
	}
	hinted := a.hints.lookup(source)
	for _, user := range hinted {
		// 提示只排顺序：已删除或被替换的用户（retired）不试，试的也照样走 AEAD。
		if user == nil || user.retired.Load() {
			continue
		}
		if aead := attempt(user); aead != nil {
			matched(user)
			return user, aead
		}
	}
	for position, user := range snapshot.list {
		if user == hinted[0] || user == hinted[1] || user == hinted[2] || user == hinted[3] {
			continue
		}
		if aead := attempt(user); aead != nil {
			matched(user)
			if position >= ssReorderDepth {
				a.maybeReorderUsers(snapshot, user.lastAuth.Load())
			}
			return user, aead
		}
	}
	return nil, nil
}

// ssSourceKey 把来源地址折成提示表的键：IPv4 用整个地址；IPv6 取 /64，
// 客户端的临时地址（隐私扩展）会在同一 /64 内轮换。拿不到 IP 时返回零值，
// 不查也不记提示。
func ssSourceKey(addr net.Addr) netip.Addr {
	var ip netip.Addr
	switch v := addr.(type) {
	case *net.TCPAddr:
		ip, _ = netip.AddrFromSlice(v.IP)
	case *net.UDPAddr:
		ip, _ = netip.AddrFromSlice(v.IP)
	case nil:
		return netip.Addr{}
	default:
		if host, _, err := net.SplitHostPort(addr.String()); err == nil {
			ip, _ = netip.ParseAddr(host)
		}
	}
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		if prefix, err := ip.Prefix(64); err == nil {
			return prefix.Addr()
		}
	}
	return ip
}

const (
	// ssHintWays 是每个来源 IP 记住的最近用户数：NAT 后面几个用户轮流建连也能命中。
	ssHintWays = 4
	// ssHintShards 分片降低锁竞争；每片两代各至多 ssHintPerGen 个来源，
	// 满了整代淘汰，总量有界（约 16×2×2048 个来源，几 MB 以内）。
	ssHintShards = 16
	ssHintPerGen = 2048
)

type ssHintEntry [ssHintWays]*ssUser

// ssSourceHints 记「来源 IP → 最近在这里认证通过的用户」，只作试解密的顺序提示。
type ssSourceHints struct {
	seed   maphash.Seed
	once   sync.Once
	shards [ssHintShards]ssHintShard
}

type ssHintShard struct {
	mu      sync.Mutex
	current map[netip.Addr]ssHintEntry
	prev    map[netip.Addr]ssHintEntry
}

func (h *ssSourceHints) shard(key netip.Addr) *ssHintShard {
	h.once.Do(func() { h.seed = maphash.MakeSeed() })
	raw := key.As16()
	return &h.shards[maphash.Bytes(h.seed, raw[:])%ssHintShards]
}

func (h *ssSourceHints) lookup(key netip.Addr) ssHintEntry {
	if !key.IsValid() {
		return ssHintEntry{}
	}
	shard := h.shard(key)
	shard.mu.Lock()
	entry, ok := shard.current[key]
	if !ok {
		entry = shard.prev[key]
	}
	shard.mu.Unlock()
	return entry
}

// remember 把 user 放到该来源的最前面（MRU），其余顺移。
func (h *ssSourceHints) remember(key netip.Addr, user *ssUser) {
	if !key.IsValid() || user == nil {
		return
	}
	shard := h.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	entry, ok := shard.current[key]
	if !ok {
		entry = shard.prev[key]
	}
	if entry[0] == user && ok {
		return
	}
	next := ssHintEntry{user}
	filled := 1
	for _, previous := range entry {
		if filled == ssHintWays {
			break
		}
		if previous != nil && previous != user && !previous.retired.Load() {
			next[filled] = previous
			filled++
		}
	}
	if shard.current == nil {
		shard.current = make(map[netip.Addr]ssHintEntry)
	}
	if _, exists := shard.current[key]; !exists && len(shard.current) >= ssHintPerGen {
		shard.prev, shard.current = shard.current, make(map[netip.Addr]ssHintEntry, ssHintPerGen)
	}
	shard.current[key] = next
}
