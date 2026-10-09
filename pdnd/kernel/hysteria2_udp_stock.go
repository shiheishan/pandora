package kernel

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// hy2 / TUIC UDP 转发借还的收发缓冲：存货表、批量名额与每用户份额。
//
// 存货表（hy2Stock）借时有存货就拿、没有才新分配，还回来一律收下，借还本身
// 从不丢弃、不重新分配；存货只在整段 hy2StockIdle 没被借过时才由后台回收。
// 所以常驻量 = 最近一段时间里同时借出的峰值，借还不随 GC 或换 P 抖动。
//
// 不用 sync.Pool：它按 P 存放，还进某个 P 私有槽的那组别的 P 拿不到，goroutine
// 换了 P 就只能新分配；每轮 GC 还会清掉一半。会话常驻缓冲去掉以后存活堆小、GC
// 频繁，批量组每组 2MB，那样每秒要重新分配上百组并清零。定长空闲表（第一轮的
// 做法）在同时借出的组数多于表长时同样边丢边分配（审查探针：1 核 8 个会话写回
// 时有停顿，2 秒新分配 2425 组、TotalAlloc 5.1GB）。

const (
	// hy2StockIdle 是存货多久没被借过就回收。
	hy2StockIdle = 30 * time.Second
)

// hy2StockEpoch 是存货时间戳的起点（单调时钟）。
var hy2StockEpoch = time.Now()

func hy2StockNow() int64 { return int64(time.Since(hy2StockEpoch)) }

type hy2Stocked[T any] struct {
	item *T
	used int64
}

// hy2Stock 是一种缓冲的存货表。后进先出：常用的几组总在顶上，底下的就是最久
// 没被借过的，回收从底下删。
type hy2Stock[T any] struct {
	mu    sync.Mutex
	items []hy2Stocked[T]
	alloc func() *T
	// allocs 是新分配的累计次数（测试断言借还不重新分配）。
	allocs atomic.Int64
}

func newHy2Stock[T any](alloc func() *T) *hy2Stock[T] {
	s := &hy2Stock[T]{alloc: alloc}
	hy2StockJanitor.register(s.trim)
	return s
}

func (s *hy2Stock[T]) get() *T {
	hy2StockJanitor.start()
	s.mu.Lock()
	if n := len(s.items); n > 0 {
		item := s.items[n-1].item
		s.items[n-1] = hy2Stocked[T]{}
		s.items = s.items[:n-1]
		s.mu.Unlock()
		return item
	}
	s.mu.Unlock()
	s.allocs.Add(1)
	return s.alloc()
}

func (s *hy2Stock[T]) put(item *T) {
	now := hy2StockNow()
	s.mu.Lock()
	s.items = append(s.items, hy2Stocked[T]{item: item, used: now})
	s.mu.Unlock()
}

// trim 回收 before 之前就没再被借过的存货。
func (s *hy2Stock[T]) trim(before int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := 0
	for keep < len(s.items) && s.items[keep].used < before {
		keep++
	}
	if keep == 0 {
		return
	}
	rest := copy(s.items, s.items[keep:])
	clear(s.items[rest:])
	s.items = s.items[:rest]
}

// size 返回当前存货数（测试用）。
func (s *hy2Stock[T]) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// hy2StockJanitor 每 hy2StockIdle 回收一次各存货表里闲置的缓冲；第一次借缓冲时
// 才起，进程内只有一个。
var hy2StockJanitor hy2Janitor

type hy2Janitor struct {
	once  sync.Once
	mu    sync.Mutex
	trims []func(before int64)
}

func (j *hy2Janitor) register(trim func(before int64)) {
	j.mu.Lock()
	j.trims = append(j.trims, trim)
	j.mu.Unlock()
}

func (j *hy2Janitor) start() {
	j.once.Do(func() {
		go func() {
			ticker := time.NewTicker(hy2StockIdle)
			defer ticker.Stop()
			for range ticker.C {
				j.run(hy2StockNow() - int64(hy2StockIdle))
			}
		}()
	})
}

func (j *hy2Janitor) run(before int64) {
	j.mu.Lock()
	trims := append([]func(before int64){}, j.trims...)
	j.mu.Unlock()
	for _, trim := range trims {
		trim(before)
	}
}

// 下行批量组（32×64KB）只能在占到名额时借：名额全局限 8×GOMAXPROCS 组，单个
// 用户最多占其中 1/4。
//
// 全局名额：写回客户端的 WritePacket 在 QUIC 连接的 DATAGRAM 发送队列（quic-go
// 每连接 32 条）满时会阻塞，借来的缓冲要一直占到它返回；连接拥塞（含客户端故意
// 不回 ACK）时，这条连接上每个有包的会话都卡着。不设上限，一个用户 1024 个会话
// 又能占回约 2GB。不卡在发送侧的批量收包同一时刻约为 GOMAXPROCS 组，8 倍留出给
// 忙碌连接排队占用的余量（4 核即 32 组、64MB）。批量组只在占到名额时借，总数
// 不超过名额，存货上限也就是名额 × 2MB。
//
// 每用户份额：名额是全局的，一个用户卡住几个会话就能占满（1 核节点 8 个），之后
// 所有用户都只能用 2 包的小组收，每次系统调用收的包少一个数量级。份额取 1/4：
// 至少 4 个用户能同时拿满，单个用户卡死自己的份额后，别的用户仍有 3/4 的名额。
// 超出份额或名额的会话照常转发，只是用小组收。
var (
	hy2DownlinkBatchSlots    = make(chan struct{}, 8*runtime.GOMAXPROCS(0))
	hy2DownlinkBatchPerUser  = int32(max(1, cap(hy2DownlinkBatchSlots)/4))
	hy2DownlinkBatchStock    = newHy2Stock(func() *hy2DownlinkGroup { return newHy2DownlinkGroup(hy2UDPBatch) })
	hy2DownlinkProbeStock    = newHy2Stock(func() *hy2DownlinkGroup { return newHy2DownlinkGroup(hy2DownlinkProbeBatch) })
	hy2UplinkBatchStock      = newHy2Stock(func() *hy2UplinkBatch { return new(hy2UplinkBatch) })
	hy2DownlinkBatchShares   = hy2BatchShares{byUser: make(map[int64]*hy2BatchShare)}
	errHy2BatchShareReleased = "hy2 batch share released twice"
)

// 热态名额：同时处在热态（持小组阻塞读）的会话全进程最多 64×GOMAXPROCS 个，
// 每用户最多其中 1/4。热态只给每秒 500 包以上的会话用：4 核节点 256 个这样的
// 会话已是每秒 12.8 万包以上，超出的会话照常在冷态收包，只多一次窥视。上限让
// 热态小组的常驻（含还回后留在存货里的）有界：4 核最多 32MB，单用户 8MB。
var (
	hy2DownlinkWarmSlots   = make(chan struct{}, 64*runtime.GOMAXPROCS(0))
	hy2DownlinkWarmPerUser = int32(max(1, cap(hy2DownlinkWarmSlots)/4))
)

// hy2BatchShare 是一个用户当前占着的批量名额与热态名额数。
type hy2BatchShare struct {
	held atomic.Int32
	warm atomic.Int32
	refs int // 在途会话数，受 hy2BatchShares.mu 保护
}

// acquireWarm 在用户份额与全局热态名额都有余时占一个热态名额。
func (share *hy2BatchShare) acquireWarm() bool {
	if share.warm.Add(1) > hy2DownlinkWarmPerUser {
		share.warm.Add(-1)
		return false
	}
	select {
	case hy2DownlinkWarmSlots <- struct{}{}:
		return true
	default:
		share.warm.Add(-1)
		return false
	}
}

func (share *hy2BatchShare) releaseWarm() {
	<-hy2DownlinkWarmSlots
	if share.warm.Add(-1) < 0 {
		panic(errHy2BatchShareReleased)
	}
}

type hy2BatchShares struct {
	mu     sync.Mutex
	byUser map[int64]*hy2BatchShare
}

// join 在会话开始时取用户的份额（同一用户的所有 hy2 / TUIC UDP 会话共用一份），
// 会话结束时 leave。
func (s *hy2BatchShares) join(userID int64) *hy2BatchShare {
	s.mu.Lock()
	defer s.mu.Unlock()
	share := s.byUser[userID]
	if share == nil {
		share = new(hy2BatchShare)
		s.byUser[userID] = share
	}
	share.refs++
	return share
}

func (s *hy2BatchShares) leave(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	share := s.byUser[userID]
	if share == nil {
		return
	}
	if share.refs--; share.refs <= 0 {
		delete(s.byUser, userID)
	}
}

// acquireBatchGroup 在份额与全局名额都有余时借一组批量组。
func (share *hy2BatchShare) acquireBatchGroup() (*hy2DownlinkGroup, bool) {
	if share.held.Add(1) > hy2DownlinkBatchPerUser {
		share.held.Add(-1)
		return nil, false
	}
	select {
	case hy2DownlinkBatchSlots <- struct{}{}:
		return hy2DownlinkBatchStock.get(), true
	default:
		share.held.Add(-1)
		return nil, false
	}
}

// releaseBatchGroup 归还批量组、名额与份额。
func (share *hy2BatchShare) releaseBatchGroup(group *hy2DownlinkGroup) {
	hy2DownlinkBatchStock.put(group)
	<-hy2DownlinkBatchSlots
	if share.held.Add(-1) < 0 {
		panic(errHy2BatchShareReleased)
	}
}
