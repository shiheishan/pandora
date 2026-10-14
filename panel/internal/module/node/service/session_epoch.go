package service

// 纪元监听与重算轮次（docs/architecture.md 第 13 节）：订阅 node:changed，另每 EpochPoll 读一次纪元值兜底；
// Redis 读不到时每 FullResync 全量比对一次。一轮对全部在线服务器各调一次 Rosters / ServerConfigs，
// 逐会话比对、只发变了的；变化密集时合并，两轮之间至少隔 MinRoundGap。

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// Run 起纪元监听与重算循环，阻塞到 ctx 结束。只调一次（serve 里 go m.Run(ctx)）。
func (m *Sessions) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if m.rdb != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.listen(ctx)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.poll(ctx)
	}()
	m.rounds(ctx)
	wg.Wait()
}

// markDirty 登记要重算的种类并唤醒重算循环。
func (m *Sessions) markDirty(users, config bool) {
	m.dirtyMu.Lock()
	m.dirtyUsers = m.dirtyUsers || users
	m.dirtyConfig = m.dirtyConfig || config
	m.dirtyMu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Sessions) takeDirty() (users, config bool) {
	m.dirtyMu.Lock()
	defer m.dirtyMu.Unlock()
	users, config = m.dirtyUsers, m.dirtyConfig
	m.dirtyUsers, m.dirtyConfig = false, false
	return users, config
}

// markByPayload 按通知载荷登记：users / config，认不出的两样都算。
func (m *Sessions) markByPayload(payload string) {
	switch payload {
	case "users":
		m.markDirty(true, false)
	case "config":
		m.markDirty(false, true)
	default:
		m.markDirty(true, true)
	}
}

// listen 订阅变更通知（go-redis 的 PubSub 断线自己重连；断开期间靠 poll 兜底）。
func (m *Sessions) listen(ctx context.Context) {
	sub := m.rdb.Subscribe(ctx, ChangedChannel)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			m.markByPayload(msg.Payload)
		}
	}
}

// poll 每 EpochPoll 读一次两个纪元值，与上次不同就重算对应的那一样；读不到时每 FullResync 全量比对一次。
func (m *Sessions) poll(ctx context.Context) {
	tick := time.NewTicker(m.opt.EpochPoll)
	defer tick.Stop()
	var lastUsers, lastConfig string
	known := false
	var lastFull time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		users, config, err := m.readEpochs(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if now := m.now(); now.Sub(lastFull) >= m.opt.FullResync {
				lastFull = now
				plog.From(ctx).Warn("读不到节点下发纪元，全量比对", zap.Error(err))
				m.markDirty(true, true)
			}
			known = false
			continue
		}
		if known {
			m.markDirty(users != lastUsers, config != lastConfig)
		} else {
			// 刚启动或 Redis 刚恢复：不知道这期间漏了什么，全量比对一次
			m.markDirty(true, true)
		}
		lastUsers, lastConfig, known = users, config, true
	}
}

// errNoRedis 表示没配 Redis：当作读不到纪元。
var errNoRedis = errors.New("node: 没有配置 Redis")

func (m *Sessions) readEpochs(ctx context.Context) (users, config string, err error) {
	if m.rdb == nil {
		return "", "", errNoRedis
	}
	vals, err := m.rdb.MGet(ctx, UsersEpochKey, ConfigEpochKey).Result()
	if err != nil {
		return "", "", err
	}
	str := func(v any) string {
		s, _ := v.(string)
		return s
	}
	return str(vals[0]), str(vals[1]), nil
}

// rounds 是重算循环：被唤醒后先满足最小间隔，再取走待办做一轮；失败的种类过 RetryDelay 再登记。
func (m *Sessions) rounds(ctx context.Context) {
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		}
		if wait := m.opt.MinRoundGap - m.now().Sub(last); !last.IsZero() && wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		users, config := m.takeDirty()
		if !users && !config {
			continue
		}
		failUsers, failConfig := m.round(ctx, users, config)
		last = m.now()
		if (failUsers || failConfig) && ctx.Err() == nil {
			time.AfterFunc(m.opt.RetryDelay, func() { m.markDirty(failUsers, failConfig) })
		}
	}
}

// round 对全部在线服务器重算一轮；返回失败的种类。
func (m *Sessions) round(ctx context.Context, users, config bool) (failUsers, failConfig bool) {
	ids := m.onlineIDs()
	if len(ids) == 0 {
		return false, false
	}
	log := plog.From(ctx)
	if config {
		seq := m.seq.Add(1)
		cfgs, err := m.b.ServerConfigs(ctx, ids)
		if err != nil {
			log.Error("重算节点配置失败", zap.Int("servers", len(ids)), zap.Error(err))
			failConfig = true
		} else {
			for _, id := range ids {
				if s := m.get(id); s != nil {
					s.applyConfig(seq, cfgs[id])
				}
			}
		}
	}
	if users {
		seq := m.seq.Add(1)
		rosters, err := m.b.Rosters(ctx, ids)
		if err != nil {
			log.Error("重算节点名单失败", zap.Int("servers", len(ids)), zap.Error(err))
			failUsers = true
		} else {
			for _, id := range ids {
				if s := m.get(id); s != nil {
					s.applyRosters(seq, rosters[id])
				}
			}
		}
	}
	return failUsers, failConfig
}
