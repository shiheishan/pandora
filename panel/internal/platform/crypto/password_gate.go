package crypto

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

//------------------------------------------------------------------------------
// 口令哈希闸门：Argon2id 的全局并发上限
//------------------------------------------------------------------------------
//
// 每次 Argon2id 要 19 MiB 工作内存。没有上限时，200 次预热登录（约 10 次/秒）
// 让 aegis-public 在 6 秒内顶满 systemd 的 MemoryMax=256M：内核把这个进程自己的
// 匿名页换出又换入（约 900 MB），持连接的事务推进极慢，连接池周转停摆，后来的
// 请求全卡在「开启事务」——登录 504、订阅被伪装成 404。
//
// 闸门把同时进行的哈希压到一个小常数（缺省 2，可配；理由见 config.DefaultPasswordHashConcurrency），排队尊重请求 ctx，
// 等不到名额就返回 ErrPasswordHashBusy，由调用方翻成 503。
//
// 用法分两种：
//   - 请求路径（identity）先 AcquirePasswordSlot 再开事务，事务里用名额的
//     Hash / Verify / DummyVerify。这样不会出现「拿着数据库连接排队等哈希名额」：
//     排队的请求不占连接，持名额的请求马上就能算。
//   - 包级 HashPassword / VerifyPassword / DummyVerify 自己取名额、不设排队超时，
//     给命令行工具与后台批量操作用。持着名额时不要再调它们（同一 goroutine 会自己
//     等自己），identity 包有守卫测试禁止调用包级入口。

// ErrPasswordHashBusy 表示在排队超时内没有等到哈希名额（或请求已取消）。
var ErrPasswordHashBusy = errors.New("口令校验繁忙，请稍后重试")

// 缺省值与 platform/config 的缺省一致；网关启动时用配置覆盖（ConfigurePasswordHashing）。
const (
	defaultPasswordHashConcurrency  = 2
	defaultPasswordHashQueueTimeout = 5 * time.Second
)

type passwordGate struct {
	slots   chan struct{}
	timeout time.Duration
}

var currentGate atomic.Pointer[passwordGate]

func init() {
	ConfigurePasswordHashing(defaultPasswordHashConcurrency, defaultPasswordHashQueueTimeout)
}

// ConfigurePasswordHashing 设定全局并发上限与排队超时。网关在开服前调用一次；
// 非法值（≤0）退回缺省。替换前已发出的名额仍归还给它们自己的那道闸门。
func ConfigurePasswordHashing(concurrency int, queueTimeout time.Duration) {
	if concurrency <= 0 {
		concurrency = defaultPasswordHashConcurrency
	}
	if queueTimeout <= 0 {
		queueTimeout = defaultPasswordHashQueueTimeout
	}
	currentGate.Store(&passwordGate{slots: make(chan struct{}, concurrency), timeout: queueTimeout})
}

// PasswordSlot 是一次口令哈希计算的许可，用完必须 Release（可重复调用）。
type PasswordSlot struct {
	gate     *passwordGate
	once     sync.Once
	released atomic.Bool
}

// AcquirePasswordSlot 等一个哈希名额：最多等排队超时，ctx 取消立即放弃。
// 等不到返回 ErrPasswordHashBusy（ctx 取消时同时包着 ctx 的错误）。
func AcquirePasswordSlot(ctx context.Context) (*PasswordSlot, error) {
	return currentGate.Load().acquire(ctx, true)
}

func (g *passwordGate) acquire(ctx context.Context, bounded bool) (*PasswordSlot, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPasswordHashBusy, err)
	}
	select {
	case g.slots <- struct{}{}:
		return &PasswordSlot{gate: g}, nil
	default:
	}
	var expired <-chan time.Time
	if bounded {
		timer := time.NewTimer(g.timeout)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case g.slots <- struct{}{}:
		return &PasswordSlot{gate: g}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrPasswordHashBusy, ctx.Err())
	case <-expired:
		return nil, ErrPasswordHashBusy
	}
}

// Release 归还名额。重复调用无副作用。
func (s *PasswordSlot) Release() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.released.Store(true)
		<-s.gate.slots
	})
}

var errSlotReleased = errors.New("口令哈希名额已归还，不能再用它计算")

// Hash 在名额内计算 PHC 串（同 HashPassword）。
func (s *PasswordSlot) Hash(password string, p Argon2Params) (string, error) {
	if s == nil || s.released.Load() {
		return "", errSlotReleased
	}
	return hashPassword(password, p)
}

// Verify 在名额内校验口令（同 VerifyPassword）。
func (s *PasswordSlot) Verify(password, phc string) (ok bool, needsRehash bool, err error) {
	if s == nil || s.released.Load() {
		return false, false, errSlotReleased
	}
	return verifyPassword(password, phc)
}

// DummyVerify 在名额内付出与一次真实校验相同的代价（同 DummyVerify）。
// 名额已归还时照样计算：它的意义就是耗时一致，不能因此提前返回。
func (s *PasswordSlot) DummyVerify(password string) {
	dummyVerify(password)
}

// withBlockingSlot 给包级入口用：取名额不设排队超时，算完即还。
func withBlockingSlot(fn func()) {
	slot, _ := currentGate.Load().acquire(context.Background(), false)
	defer slot.Release()
	fn()
}
