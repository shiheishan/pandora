package notify

import (
	"context"
	"sync"
	"time"
)

// 扫描与派发的节拍。
//
// 两件事拆成两个 goroutine：扫描是全租户的（到期、流量、付款三类），一轮可能要
// 好几秒；派发要逐封发信，SMTP 慢的时候一封就是几十秒。原先两者在同一个循环里
// 串行，注册验证码的 Kick 也排在同一个循环上：扫描一慢，验证码就跟着等（队头阻塞），
// 而派发每 5 分钟才一轮、每轮 100 条，批量到期提醒要排几个小时。
const (
	// scanWarmup：进程刚起来时连接池、缓存都还没热，先等一会儿再扫全表
	scanWarmup = 30 * time.Second
	// scanRoundTimeout：一轮扫描的上限，扫不完下一轮接着来（各扫描按 dedupe_key 幂等）
	scanRoundTimeout = 2 * time.Minute

	// dispatchEvery：派发的常规节拍；扫描排完队、注册排了验证码都会 Kick 提前一轮
	dispatchEvery = 30 * time.Second
	// dispatchBatch / dispatchMaxBatches：每批认领多少条、一轮至多几批。一轮内循环到
	// 队列空为止（有上限），积压不会只靠每 5 分钟 100 条慢慢消化
	dispatchBatch      = 20
	dispatchMaxBatches = 50
	// dispatchRoundTimeout 必须短于认领租约（dispatchLease）：一轮里认领到的行要么发完、
	// 要么随本轮超时放弃，放弃的等租约过期由任一实例重新认领，不会两个实例同时在发
	dispatchRoundTimeout = 4 * time.Minute
)

// StartScanner 起扫描与派发两个后台循环，返回等它们退出的函数（ctx 取消后调用）。
func (s *Service) StartScanner(ctx context.Context, tenantID string, every time.Duration) (wait func()) {
	var wg sync.WaitGroup
	wg.Add(2)

	// 扫描：预热之后每 every 一轮，排完队就催派发
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			return
		case <-time.After(scanWarmup):
		}
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			s.scanRound(ctx, tenantID)
			s.Kick()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// 派发：常规节拍 + Kick 提前；与扫描互不等待
	go func() {
		defer wg.Done()
		t := time.NewTicker(dispatchEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-s.kick:
			}
			s.dispatchRound(ctx, tenantID)
		}
	}()

	return wg.Wait
}

func (s *Service) scanRound(ctx context.Context, tenantID string) {
	ctx, cancel := context.WithTimeout(ctx, scanRoundTimeout)
	defer cancel()
	if n, err := s.ScanExpiring(ctx, tenantID); err != nil {
		s.log.Warn("到期扫描失败", "err", err)
	} else if n > 0 {
		s.log.Info("到期提醒已排队", "条数", n)
	}
	if n, err := s.ScanQuota(ctx, tenantID); err != nil {
		s.log.Warn("流量扫描失败", "err", err)
	} else if n > 0 {
		s.log.Info("流量预警已排队", "条数", n)
	}
	if n, err := s.ScanPaidOrders(ctx, tenantID); err != nil {
		s.log.Warn("支付通知扫描失败", "err", err)
	} else if n > 0 {
		s.log.Info("支付通知已排队", "条数", n)
	}
}

// dispatchRound 一批一批认领并投递，直到队列空、批数用完或本轮超时。
func (s *Service) dispatchRound(ctx context.Context, tenantID string) {
	ctx, cancel := context.WithTimeout(ctx, dispatchRoundTimeout)
	defer cancel()
	total := 0
	for i := 0; i < dispatchMaxBatches; i++ {
		n, err := s.Dispatch(ctx, tenantID, dispatchBatch)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("通知派发失败", "err", err)
			}
			break
		}
		total += n
		if n < dispatchBatch {
			break
		}
	}
	if total > 0 {
		s.log.Info("通知已派发", "条数", total)
	}
}
