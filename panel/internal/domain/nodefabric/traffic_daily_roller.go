package nodefabric

import (
	"context"
	"sync"
	"time"
)

// 节点 × uid 按天汇总的进度（进程内）。
//
// 原先 RefreshTrafficDaily 每 10 分钟用一条语句重扫保留期内的 70 天，找「按天表里还没有、
// 小时表里有」的日子：静默时也要读 80 多万个缓冲块、占 2 秒（w10quiet 的 pgss），而 99% 的轮次
// 什么也找不到。现在记着进度，只在两种时机核对库：
//   - 进程启动后的第一轮：补洞。上线或停机期间留下的、没汇总的历史日子都在这一次找出来，
//     放进待办，之后每轮从新到旧补 trafficDailyMaxDaysPerRun 天；
//   - 每个 UTC 日界（上一个自然日结束满 10 分钟）之后的第一轮：同样核对一遍窗口，新结束的
//     那一天进待办。整窗口再核对一遍而不只看新的一天，是为了让漏网的日子（别的实例没写完、
//     人工补过小时数据）最多一天内被自己发现，不必等重启。
//
// 其余的轮次只比较时钟，不碰库。核对用一批按天的存在性探测（trafficDayNeedsRollupSQL），
// 一次往返，不再有那条随表增长的扫描。
//
// 一天只会在结束 10 分钟之后才汇总，那时这一天的上报事务早已提交，小时表里这一天不会再有
// 写入，所以「核对过、当时没有数据」的日子不会事后变得需要汇总。待办里的日子汇总失败就留着，
// 下一轮接着做；汇总本身是 DO NOTHING，多实例同时做也只写一份。

// trafficDailyEndMargin 是一个 UTC 日结束之后再等多久才汇总（小时桶按 received_at 切，
// 那时这一天的上报事务早已提交）。
const trafficDailyEndMargin = 10 * time.Minute

// trafficDailyStore 是进度机器用到的库操作，*Service 实现；拆成接口是为了不连库就能测进度逻辑。
type trafficDailyStore interface {
	// trafficDailyPendingDays 返回 [first, last] 内要汇总的 UTC 日，从新到旧。
	trafficDailyPendingDays(ctx context.Context, tenantID string, first, last time.Time) ([]time.Time, error)
	// rollupTrafficDay 汇总一个 UTC 日，返回写入的行数。
	rollupTrafficDay(ctx context.Context, tenantID string, day time.Time) (int64, error)
}

type trafficDailyProgress struct {
	// checkedThrough 是核对过的最后一个已结束 UTC 日（零点）：窗口内此日及以前的缺口都在 pending 里。
	checkedThrough time.Time
	// pending 是要汇总的日子，从新到旧。
	pending []time.Time
}

// trafficDailyRoller 按租户记着进度。零值可用；now 为空时用 time.Now（测试里换成假时钟）。
type trafficDailyRoller struct {
	now     func() time.Time
	mu      sync.Mutex
	tenants map[string]*trafficDailyProgress
	// checks 是核对库的次数（测试用：稳态下应当不再增长）
	checks int
}

func (r *trafficDailyRoller) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// trafficDailyWindow 返回现在可以汇总的 UTC 日范围 [first, last]（零点）：
//   - last 是已结束 10 分钟以上的最后一天；
//   - first 是整天都还在节点 × uid 小时表保留期内的第一天，不会汇总出被清理截掉一半的一天。
//     在 70 天线的基础上多留一天余量：保留期清理按会话时区算「70 天」，遇到夏令时与 UTC 的 24 小时
//     日界差一小时，余量保证不会取到刚被清掉第一个小时的那一天。
//
// first 晚于 last 时窗口为空。
func trafficDailyWindow(now time.Time) (first, last time.Time) {
	last = utcDay(now.Add(-(24*time.Hour + trafficDailyEndMargin)))
	first = utcDay(now.Add(-UserTrafficHourlyRetentionDays * 24 * time.Hour)).Add(48 * time.Hour)
	return first, last
}

// refresh 做一轮：必要时核对库、再从待办里汇总至多 trafficDailyMaxDaysPerRun 天。返回写入的行数。
// 核对失败时进度不变，下一轮重来；某天汇总失败时它和更旧的日子留在待办里。
func (r *trafficDailyRoller) refresh(ctx context.Context, store trafficDailyStore, tenantID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	first, last := trafficDailyWindow(r.clock())
	if last.Before(first) {
		return 0, nil
	}
	prog := r.tenants[tenantID]
	if prog == nil || prog.checkedThrough.Before(last) {
		pending, err := store.trafficDailyPendingDays(ctx, tenantID, first, last)
		r.checks++
		if err != nil {
			return 0, err
		}
		prog = &trafficDailyProgress{checkedThrough: last, pending: pending}
		if r.tenants == nil {
			r.tenants = make(map[string]*trafficDailyProgress)
		}
		r.tenants[tenantID] = prog
	}
	// 掉出窗口的日子不再汇总（待办按新到旧排，掉出的在尾部）
	for len(prog.pending) > 0 && prog.pending[len(prog.pending)-1].Before(first) {
		prog.pending = prog.pending[:len(prog.pending)-1]
	}
	var total int64
	for done := 0; done < trafficDailyMaxDaysPerRun && len(prog.pending) > 0; done++ {
		n, err := store.rollupTrafficDay(ctx, tenantID, prog.pending[0])
		if err != nil {
			return total, err
		}
		total += n
		prog.pending = prog.pending[1:]
	}
	return total, nil
}
