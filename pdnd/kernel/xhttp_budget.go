package kernel

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// XHTTP 会话型模式（packet-up / stream-up，auto 的缺省分支）的资源上限。
//
// 会话由客户端随手造一个 id 就建起来，认证要等协议请求头（seq 0）到齐才做。
// 未认证的客户端只要只发 seq≥1 的包，每个会话就能钉住 sc_max_buffered_posts ×
// sc_max_each_post_bytes（缺省 30 × 1MB），会话数又不设限，直到 10 秒请求头截止
// 才释放——审查实测 20 个会话 heap 涨 219MB，节点机只有 1c1g。所以：
//   - 每个入站限制会话总数与未认证会话数；
//   - 未认证会话的待读上行字节按会话、按入站各封一个顶，超出即拒（503）；
//   - 协议层读完请求头（清掉读截止）即视为认证，此后只受包数上限约束。

var (
	// errXHTTPCapacity：会话数或未认证字节预算用尽，回 503、不带正文。
	errXHTTPCapacity = errors.New("xhttp capacity exceeded")
	// errXHTTPConflict：同一会话的第二条上行（Xray 对第二条 stream-up 回 409）。
	errXHTTPConflict = errors.New("xhttp session already has an uplink")
)

const (
	// xhttpBrokerIdle 是 broker.GC 回收空闲会话的时长。
	xhttpBrokerIdle = 5 * time.Minute
	// xhttpMaxSessions 是单个入站同时存在的会话型连接上限。
	xhttpMaxSessions = 8192
	// xhttpMaxUnauthSessions 是同时处于「已建会话、尚未认证」的会话上限。正常
	// 客户端在这个状态只停留一个往返。
	xhttpMaxUnauthSessions = 256
	// 未认证字节预算按 sc_max_each_post_bytes 的倍数给：单会话 2 个包（seq 0 与
	// 并发发出的下一个包可能乱序到达），整个入站 16 个包。
	xhttpUnauthSessionPosts = 2
	xhttpUnauthTotalPosts   = 16
)

// xhttpByteBudget 是一个入站所有未认证会话共享的待读字节预算。
type xhttpByteBudget struct {
	mu    sync.Mutex
	used  int64
	limit int64
}

func (b *xhttpByteBudget) take(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.limit {
		return false
	}
	b.used += n
	return true
}

func (b *xhttpByteBudget) give(n int64) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
}

type xhttpSessionLimits struct {
	maxSessions        int
	maxUnauth          int
	unauthSessionBytes int64
	budget             *xhttpByteBudget
}

func (l xhttpSessionLimits) admit(sessions, unauth int) error {
	if l.maxSessions > 0 && sessions >= l.maxSessions {
		return fmt.Errorf("%w: %d sessions", errXHTTPCapacity, sessions)
	}
	if l.maxUnauth > 0 && unauth >= l.maxUnauth {
		return fmt.Errorf("%w: %d unauthenticated sessions", errXHTTPCapacity, unauth)
	}
	return nil
}

// newXHTTPSessionBroker 建入站用的会话中转，按配置的单包上限算未认证预算。
func newXHTTPSessionBroker(cfg XHTTPConfig) (*XHTTPPacketBroker, error) {
	broker, err := NewXHTTPPacketBroker(cfg.MaxBufferedPosts, xhttpBrokerIdle)
	if err != nil {
		return nil, err
	}
	post := int64(cfg.MaxPost.To)
	if post <= 0 {
		post = 1_000_000
	}
	broker.limits = xhttpSessionLimits{
		maxSessions:        xhttpMaxSessions,
		maxUnauth:          xhttpMaxUnauthSessions,
		unauthSessionBytes: xhttpUnauthSessionPosts * post,
		budget:             &xhttpByteBudget{limit: xhttpUnauthTotalPosts * post},
	}
	return broker, nil
}

// Authenticated 在协议层认证通过后调用：会话不再计入未认证上限与字节预算。
func (b *XHTTPPacketBroker) Authenticated(id string) {
	b.mu.Lock()
	session := b.sessions[id]
	if session == nil || !session.unauth {
		b.mu.Unlock()
		return
	}
	session.unauth = false
	b.unauth--
	b.mu.Unlock()
	session.uplink.leaveBudget()
}
