package realtime

import (
	"context"
	"encoding/json"
	"errors"
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

// subscriber 是一条已建立的 SSE 连接。
//
// ch 永不关闭：注销只关 done。dispatch 先复制目标列表、解锁后再发送，
// 注销若在这两步之间 close(ch)，发送就会 panic（send on closed channel），
// 而 consume 没有 recover，整个网关进程跟着崩。
//
// revoked 单独一条信号：会话吊销不能和普通事件挤同一个缓冲，缓冲满时被丢掉，
// 已吊销的会话就会一直挂着收事件。
type subscriber struct {
	ch         chan Event
	done       chan struct{}
	once       sync.Once
	revoked    chan struct{}
	revokeOnce sync.Once
	channels   map[string]bool
	owner      uuid.UUID
}

// Subscription 是 Subscribe 的结果。
type Subscription struct {
	// Events 是推给这条连接的事件，永不关闭。
	Events <-chan Event
	// Revoked 在会话被吊销时关闭，连接应立即结束。
	Revoked <-chan struct{}
	// Close 注销连接，可重复调用。
	Close func()
}

// Hub 管理本机的所有 SSE 连接，并通过 Redis 与其它进程互通。
type Hub struct {
	rdb        *redis.Client
	log        *zap.Logger
	maxStreams int

	mu   sync.RWMutex
	subs map[*subscriber]struct{}
	// byChannel 是订阅索引。没有它的话每次广播都要遍历全部连接。
	byChannel map[string]map[*subscriber]struct{}
	byOwner   map[uuid.UUID]int

	// ready 在 Redis 确认模式订阅后关闭：之前发出的消息这台进程收不到（pub/sub 不留存）。
	ready     chan struct{}
	readyOnce sync.Once

	ctx    context.Context
	cancel context.CancelFunc
}

// NewHub 构造 Hub 并启动 Redis 消费；rdb 为 nil 时只做本机分发（单测用）。
func NewHub(rdb *redis.Client, log *zap.Logger, maxStreams int) *Hub {
	if log == nil {
		log = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		rdb: rdb, log: log, maxStreams: maxStreams,
		subs:      make(map[*subscriber]struct{}),
		byChannel: make(map[string]map[*subscriber]struct{}),
		byOwner:   make(map[uuid.UUID]int),
		ready:     make(chan struct{}),
		ctx:       ctx, cancel: cancel,
	}
	if rdb != nil {
		go h.consume()
	} else {
		h.markReady()
	}
	return h
}

// Close 停止 Redis 消费。
func (h *Hub) Close() { h.cancel() }

func (h *Hub) markReady() { h.readyOnce.Do(func() { close(h.ready) }) }

// subscribe 发起模式订阅并最多等 5 秒 Redis 确认；没等到也照样返回，
// 后面的 ReceiveMessage 出错会走重连，不让消费卡死在确认上。
func (h *Hub) subscribe() *redis.PubSub {
	ps := h.rdb.PSubscribe(h.ctx, "rt:*")
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	if _, err := ps.Receive(ctx); err == nil {
		h.markReady()
	} else if h.ctx.Err() == nil {
		h.log.Warn("实时广播订阅未确认", zap.Error(err))
	}
	return ps
}

// consume 收 Redis 上的广播并分发给本机连接。
//
// 中断后不能退出：一旦退出，本机所有连接都再也收不到事件，而它们的 SSE 看起来还好好的，故障完全静默。
func (h *Hub) consume() {
	ps := h.subscribe()
	defer func() { _ = ps.Close() }()

	for {
		select {
		case <-h.ctx.Done():
			return
		default:
		}
		msg, err := ps.ReceiveMessage(h.ctx)
		if err != nil {
			if h.ctx.Err() != nil {
				return
			}
			h.log.Warn("实时广播订阅中断，正在重连", zap.Error(err))
			_ = ps.Close()
			time.Sleep(2 * time.Second)
			if h.ctx.Err() != nil {
				return
			}
			ps = h.subscribe()
			continue
		}
		// 会话吊销由 auth 的 Lua 发出，载荷是字符串 revoked，不是 JSON
		if strings.HasPrefix(msg.Channel, "rt:sess:") {
			h.dispatch(msg.Channel, Event{Topic: topicSessionRevoked})
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
			continue
		}
		h.dispatch(msg.Channel, ev)
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
			s.revokeOnce.Do(func() { close(s.revoked) })
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

	if h.maxStreams > 0 && len(h.subs) >= h.maxStreams {
		return nil, ErrTooManyStreams
	}
	if owner != uuid.Nil && h.byOwner[owner] >= MaxStreamsPerUser {
		return nil, ErrTooManyUserStreams
	}

	s := &subscriber{
		ch:       make(chan Event, 32),
		done:     make(chan struct{}),
		revoked:  make(chan struct{}),
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
	return &Subscription{Events: s.ch, Revoked: s.revoked, Close: closeFn}, nil
}

// Count 返回本机当前的连接数，用于观测。
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
