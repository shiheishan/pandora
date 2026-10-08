package seed

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// adminIPBase 是多会话造数时各会话的虚构来源地址所在网段（RFC 5737 文档网段 198.51.100.0/24）：
// 第 w 个会话（从 0 起）用 .(w+1)。users 子命令的后台流量缺省用 .200，两者不会重叠。
const adminIPBase = "198.51.100."

// maxAdminWorkers 受登录接口限制：后台登录按路由整体每分钟限 AEGIS_RL_AUTH_PER_MIN 次（缺省 10），
// 会话开多了登录本身就要排队；32 已远超造 1500 个节点的需要。
const maxAdminWorkers = 32

func adminWorkerIP(w int) string { return fmt.Sprintf("%s%d", adminIPBase, w+1) }

// adminPool 是 N 个后台会话：每个会话一条独立登录、独立节流（相邻请求至少隔 interval）、
// 独立虚构来源 IP。后台限流按 IP 计，N 个会话合起来的吞吐是单会话的 N 倍，
// 走的仍是同一套后台网关、同一套权限与审计，只是并行。
//
// 只有一个会话时不带来源头，行为与改动前完全一致（来源就是连接本身）。
type adminPool struct {
	clients []*adminClient
}

func newAdminPool(base, email, password string, interval time.Duration, workers int, ipHeaders []string) *adminPool {
	p := &adminPool{}
	for w := 0; w < max(workers, 1); w++ {
		c := newAdminClient(base, email, password, interval)
		if workers > 1 {
			c.ip, c.ipHeaders = adminWorkerIP(w), ipHeaders
		}
		p.clients = append(p.clients, c)
	}
	return p
}

// primary 是第一个会话：目录、套餐发布、退役这些本来就是串行的步骤用它。
func (p *adminPool) primary() *adminClient { return p.clients[0] }

// login 依次登录全部会话。登录接口按路由整体限流，不并发打。
func (p *adminPool) login(ctx context.Context) error {
	for i, c := range p.clients {
		if err := c.login(ctx); err != nil {
			return fmt.Errorf("session %d (%s): %w", i+1, c.ip, err)
		}
	}
	return nil
}

// Calls 是全部会话发出的请求总数（含重试）。
func (p *adminPool) Calls() int {
	total := 0
	for _, c := range p.clients {
		c.mu.Lock()
		total += c.Calls
		c.mu.Unlock()
	}
	return total
}

// forEach 把 0..n-1 分给各会话并行跑 fn，每个会话同一时刻只有一个在途请求（节流本来就是串行的）。
// 第一个错误取消其余并返回；fn 自己按下标把结果写进预分配的切片，各下标互不重叠。
func (p *adminPool) forEach(ctx context.Context, n int, fn func(ctx context.Context, c *adminClient, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	for _, c := range p.clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := fn(ctx, c, i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
				}
			}
		}()
	}
feed:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
