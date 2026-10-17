package service

// 建流对齐合批（组提交）：同一时段到达的建流共用一次配置与名单计算。名单的读（Rosters）不管几台服务器都要把
// 全部可服务订阅、账号核对、版本、池读一遍，逐台读时面板重启后几百台节点一起重连，这些读会重复几百遍；
// 合批后一批只读一次（ServerConfigs、Rosters 各一次，参数是这批的全部服务器）。
//
//   - 到达的请求排队；没有在跑的批时由它起一个 goroutine。goroutine 一次跑一批：取走当时排队的全部请求
//     （服务器 id 去重），读完把各自那份结果交给各个等待者；计算期间新到的排进下一批；队列空了就退出。
//     不依赖 Run：纪元循环没起来时建流照样能对齐。
//   - 批是串行的，同时只有一批在读库，不会占满连接池、拖住后台与鉴权。
//   - 取号同纪元轮次：先取配置序号读配置，再取名单序号读名单（同一批里 cfgSeq < usersSeq）；与纪元轮次交错时
//     靠序号丢掉旧结果（session.go 文件头）。请求先排队、批后取号，所以一条流登记前已提交的变更，这批一定读得到。
//   - 期限：一批读库以 AlignTimeout 为限，从这批第一条请求的 ctx 派生但不随它取消（context.WithoutCancel，
//     logger、req_id 等值照留，库里的慢查询日志记在这条流名下）：一条流断开不影响同批其他流。
//     等待者另有自己的期限（awaitAlign），到期就走，这批的结果对它丢弃（结果通道带一格缓冲，交付不阻塞）。

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/google/uuid"
	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// alignQueue 是建流对齐的排队状态。
type alignQueue struct {
	mu      sync.Mutex
	pending []*alignReq
	// running 为真：有一个 goroutine 在跑批，pending 里的请求由它接着取走
	running bool
}

// alignReq 是一条流的对齐请求。
type alignReq struct {
	// ctx 是流的 ctx，只取它的值（logger、req_id）；批的期限另算
	ctx      context.Context
	serverID uuid.UUID
	// res 带一格缓冲：等待者走了也不阻塞交付
	res chan alignResult
}

// alignResult 是一台服务器的对齐结果；err 非 nil 时其余字段无意义。
type alignResult struct {
	cfgSeq, usersSeq uint64
	cfgs             []NodeConfigDoc
	rosters          []NodeRoster
	err              error
}

// alignBatchResult 是一批读到的配置与名单（服务器 id → 结果）及各自的序号。
type alignBatchResult struct {
	cfgSeq, usersSeq uint64
	cfgs             map[uuid.UUID][]NodeConfigDoc
	rosters          map[uuid.UUID][]NodeRoster
}

// awaitAlign 把这台服务器交给对齐器并等这批的结果。actx 是等待者的期限（OpenVerified 从开始对齐算起的 AlignTimeout）：
// 到期回 ErrAlignBusy，流的 ctx 先结束回 ctx.Err()；走了之后这批的结果对它丢弃。
func (m *Sessions) awaitAlign(ctx, actx context.Context, serverID uuid.UUID) (alignResult, error) {
	if actx.Err() != nil {
		// 记 Hello 已用完期限，或流已断：不再排进批
		return alignResult{}, alignGiveUp(ctx)
	}
	res := m.requestAlign(ctx, serverID)
	select {
	case r := <-res:
		return r, r.err
	case <-actx.Done():
		return alignResult{}, alignGiveUp(ctx)
	}
}

// alignGiveUp 是等待者到期时的错误：流的 ctx 已结束回 ctx.Err()，否则是 AlignTimeout 到期，回 ErrAlignBusy。
func alignGiveUp(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrAlignBusy
}

// requestAlign 把这台服务器排进下一批，返回接结果的通道；没有在跑的批时起一个 goroutine 跑。
func (m *Sessions) requestAlign(ctx context.Context, serverID uuid.UUID) <-chan alignResult {
	req := &alignReq{ctx: ctx, serverID: serverID, res: make(chan alignResult, 1)}
	q := &m.align
	q.mu.Lock()
	q.pending = append(q.pending, req)
	start := !q.running
	q.running = true
	q.mu.Unlock()
	if start {
		go m.alignLoop()
	}
	return req.res
}

// alignLoop 一批接一批地跑，队列空了就退出（running 在同一把锁里复位，之后到达的请求会再起一个）。
func (m *Sessions) alignLoop() {
	q := &m.align
	for {
		q.mu.Lock()
		batch := q.pending
		q.pending = nil
		if len(batch) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()
		m.alignBatch(batch)
	}
}

// alignBatch 为一批请求读一次配置与名单（服务器 id 去重），把各自那份交给各个等待者；读失败时这批的等待者都拿到错误。
func (m *Sessions) alignBatch(batch []*alignReq) {
	ids := make([]uuid.UUID, 0, len(batch))
	for _, r := range batch {
		ids = append(ids, r.serverID)
	}
	slices.SortFunc(ids, sessionCmpID)
	ids = slices.Compact(ids)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(batch[0].ctx), m.opt.AlignTimeout)
	defer cancel()
	res, err := m.readAlignBatch(ctx, ids)
	if err != nil && ctx.Err() != nil {
		// 这批没在 AlignTimeout 内读完
		err = ErrAlignBusy
	}
	for _, r := range batch {
		if err != nil {
			r.res <- alignResult{err: err}
			continue
		}
		r.res <- alignResult{
			cfgSeq: res.cfgSeq, usersSeq: res.usersSeq,
			cfgs: res.cfgs[r.serverID], rosters: res.rosters[r.serverID],
		}
	}
}

// readAlignBatch 先取号读配置、再取号读名单（与纪元轮次同序）。读的过程中 panic 时记日志并转成错误交给这批的等待者：
// 逐流读时由 gRPC 拦截器兜住，合批后在对齐 goroutine 里，不兜住会带崩整个进程。
func (m *Sessions) readAlignBatch(ctx context.Context, ids []uuid.UUID) (res alignBatchResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			plog.From(ctx).Error("建流对齐读取 panic", zap.Int("servers", len(ids)), zap.Any("panic", p), zap.Stack("stack"))
			err = fmt.Errorf("node: 建流对齐读取 panic: %v", p)
		}
	}()
	res.cfgSeq = m.seq.Add(1)
	if res.cfgs, err = m.b.ServerConfigs(ctx, ids); err != nil {
		return res, err
	}
	res.usersSeq = m.seq.Add(1)
	res.rosters, err = m.b.Rosters(ctx, ids)
	return res, err
}
