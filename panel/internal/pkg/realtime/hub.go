package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// MaxStreamsPerUser 是同一用户在本机可同时挂着的 SSE 条数上限；
// 一个人多开几个标签页够用，防单个账号占满。
const MaxStreamsPerUser = 10

// ErrTooManyStreams 是本机 SSE 总连接数已满。
var ErrTooManyStreams = errors.New("too many sse streams")

// ErrTooManyUserStreams 是该用户在本机的 SSE 连接数已满。
var ErrTooManyUserStreams = errors.New("too many sse streams for user")

// ErrNotLive 是广播订阅还没建立（启动中、断线重连中或已关闭）：此时接进来的连接收不到吊销，先拒掉。
var ErrNotLive = errors.New("realtime subscription not live")

const (
	// defaultHealthInterval 是订阅连接多久没消息就发一次 PING；PING 再过一个间隔没回就判断连接已坏、重建订阅。
	// 防半开连接：没有读期限时只能等 TCP keepalive，要几十分钟，期间本机所有连接静默收不到事件。
	defaultHealthInterval = 30 * time.Second
	// resubscribeDelay 是订阅出错后重建前的等待。
	resubscribeDelay = 2 * time.Second
)

// subscriber 是一条已建立的 SSE 连接。
//
// ch 永不关闭：注销只关 done。dispatch 先复制目标列表、解锁后再发送，
// 注销若在这两步之间 close(ch)，发送就会 panic（send on closed channel），
// 而 consume 没有 recover，整个网关进程跟着崩。
//
// end 单独一条信号：会话吊销、订阅中断、Hub 关闭都要让连接结束，
// 不能和普通事件挤同一个缓冲，缓冲满时被丢掉，连接就会一直挂着。
type subscriber struct {
	ch       chan Event
	done     chan struct{}
	once     sync.Once
	end      chan struct{}
	endOnce  sync.Once
	channels map[string]bool
	owner    uuid.UUID
}

func (s *subscriber) finish() { s.endOnce.Do(func() { close(s.end) }) }

// Subscription 是 Subscribe 的结果。
type Subscription struct {
	// Events 是推给这条连接的事件，永不关闭。
	Events <-chan Event
	// Ended 关闭表示连接应立即结束：会话被吊销、广播订阅中断（期间的事件与吊销可能丢了）或 Hub 关闭。
	Ended <-chan struct{}
	// Close 注销连接，可重复调用。
	Close func()
}

// Hub 管理本机的所有 SSE 连接，并通过 Redis 与其它进程互通。
type Hub struct {
	rdb        *redis.Client
	log        *zap.Logger
	maxStreams int
	// health 是健康检查间隔，单测调短
	health time.Duration

	mu   sync.RWMutex
	subs map[*subscriber]struct{}
	// byChannel 是订阅索引。没有它的话每次广播都要遍历全部连接。
	byChannel map[string]map[*subscriber]struct{}
	byOwner   map[uuid.UUID]int
	// live 表示 Redis 已确认模式订阅且之后没断过；pub/sub 不留存，不 live 时发出的消息本机收不到。
	// 和登记表同一把锁：连接要么在下线前登记（下线时被结束），要么在下线后到来（被拒）。
	live bool

	ctx    context.Context
	cancel context.CancelFunc
}

// NewHub 构造 Hub 并启动 Redis 消费；rdb 为 nil 时只做本机分发（单测用），直接视为 live。
func NewHub(rdb *redis.Client, log *zap.Logger, maxStreams int) *Hub {
	return newHub(rdb, log, maxStreams, defaultHealthInterval)
}

func newHub(rdb *redis.Client, log *zap.Logger, maxStreams int, health time.Duration) *Hub {
	if log == nil {
		log = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		rdb: rdb, log: log, maxStreams: maxStreams, health: health,
		subs:      make(map[*subscriber]struct{}),
		byChannel: make(map[string]map[*subscriber]struct{}),
		byOwner:   make(map[uuid.UUID]int),
		live:      rdb == nil,
		ctx:       ctx, cancel: cancel,
	}
	if rdb != nil {
		go h.consume()
	}
	return h
}

// Close 停止 Redis 消费并让本机所有连接结束；停机时先调它，HTTP Shutdown 才不会被长连接拖到超时。
func (h *Hub) Close() {
	h.cancel()
	h.goDown()
}

// Live 报告广播订阅是否正常（系统状态页用）。
func (h *Hub) Live() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.live
}

func (h *Hub) goLive() {
	h.mu.Lock()
	h.live = h.ctx.Err() == nil
	h.mu.Unlock()
}

// goDown 置为不 live，并让当前所有连接结束：客户端会带令牌重连，鉴权重做一遍，并把数据全部重拉。
func (h *Hub) goDown() {
	h.mu.Lock()
	h.live = false
	targets := make([]*subscriber, 0, len(h.subs))
	for s := range h.subs {
		targets = append(targets, s)
	}
	h.mu.Unlock()
	for _, s := range targets {
		s.finish()
	}
}

// subscribe 发起模式订阅并等 Redis 确认（一个健康间隔为限）。
func (h *Hub) subscribe() (*redis.PubSub, error) {
	ps := h.rdb.PSubscribe(h.ctx, "rt:*")
	if _, err := ps.ReceiveTimeout(h.ctx, h.health); err != nil {
		_ = ps.Close()
		return nil, err
	}
	return ps, nil
}

// consume 收 Redis 上的广播并分发给本机连接。
//
// 中断后不能退出：一旦退出，本机所有连接都再也收不到事件，而它们的 SSE 看起来还好好的，故障完全静默。
// 订阅断过就让本机连接全部结束：断线期间的事件和会话吊销都丢了，只有重连才能重新鉴权、重拉数据。
func (h *Hub) consume() {
	for {
		ps, err := h.subscribe()
		if err == nil {
			h.goLive()
			err = h.receive(ps)
			h.goDown()
			_ = ps.Close()
		}
		if h.ctx.Err() != nil {
			return
		}
		h.log.Warn("实时广播订阅中断，稍后重连", zap.Error(err))
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(resubscribeDelay):
		}
	}
}

// receive 读到出错为止；空闲一个间隔就发 PING，再一个间隔仍无任何回包就返回错误。
func (h *Hub) receive(ps *redis.PubSub) error {
	pinged := false
	for {
		msg, err := ps.ReceiveTimeout(h.ctx, h.health)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && !pinged && h.ctx.Err() == nil {
				if err := ps.Ping(h.ctx); err != nil {
					return err
				}
				pinged = true
				continue
			}
			return err
		}
		pinged = false
		m, ok := msg.(*redis.Message)
		if !ok {
			continue // Pong、订阅确认
		}
		// 会话吊销由 auth 的 Lua 发出，载荷是字符串 revoked，不是 JSON
		if strings.HasPrefix(m.Channel, "rt:sess:") {
			h.dispatch(m.Channel, Event{Topic: topicSessionRevoked})
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(m.Payload), &ev); err != nil {
			continue
		}
		h.dispatch(m.Channel, ev)
	}
}

func (h *Hub) dispatch(channel string, ev Event) {
	h.mu.RLock()
	targets := make([]*subscriber, 0, 8)
	for s := range h.byChannel[channel] {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		select {
		case <-s.done:
			continue
		default:
		}
		if ev.Topic == topicSessionRevoked {
			s.finish()
			continue
		}
		select {
		case s.ch <- ev:
		case <-s.done:
		default:
			// 这条连接的缓冲满了 —— 客户端读得太慢或已经僵死。直接丢弃。
		}
	}
}

// Subscribe 登记一条连接。
func (h *Hub) Subscribe(owner uuid.UUID, channels []string) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.live {
		return nil, ErrNotLive
	}
	if h.maxStreams > 0 && len(h.subs) >= h.maxStreams {
		return nil, ErrTooManyStreams
	}
	if owner != uuid.Nil && h.byOwner[owner] >= MaxStreamsPerUser {
		return nil, ErrTooManyUserStreams
	}

	s := &subscriber{
		ch:       make(chan Event, 32),
		done:     make(chan struct{}),
		end:      make(chan struct{}),
		channels: make(map[string]bool, len(channels)),
		owner:    owner,
	}

	h.subs[s] = struct{}{}
	if owner != uuid.Nil {
		h.byOwner[owner]++
	}
	for _, c := range channels {
		s.channels[c] = true
		if h.byChannel[c] == nil {
			h.byChannel[c] = make(map[*subscriber]struct{})
		}
		h.byChannel[c][s] = struct{}{}
	}

	closeFn := func() {
		s.once.Do(func() {
			h.mu.Lock()
			delete(h.subs, s)
			if s.owner != uuid.Nil {
				if n := h.byOwner[s.owner]; n <= 1 {
					delete(h.byOwner, s.owner)
				} else {
					h.byOwner[s.owner] = n - 1
				}
			}
			for c := range s.channels {
				if set := h.byChannel[c]; set != nil {
					delete(set, s)
					if len(set) == 0 {
						delete(h.byChannel, c)
					}
				}
			}
			h.mu.Unlock()
			close(s.done)
		})
	}
	return &Subscription{Events: s.ch, Ended: s.end, Close: closeFn}, nil
}

// Count 返回本机当前的连接数，用于观测。
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
