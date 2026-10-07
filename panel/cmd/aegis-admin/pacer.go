package main

import (
	"math/rand/v2"
	"time"
)

// loopPacer 给 admin 的后台循环定节拍：首轮随机延迟，之后每轮间隔带 ±10% 抖动。
//
// 原先五个循环都是启动即跑、之后按固定 Ticker：同一进程里它们的相位完全相同，
// 每 10 分钟有四个事务在同一毫秒开始，每小时再叠上佣金解冻；三个网关同时重启时
// 还会和 public / node 的循环撞在同一秒（5k-r3 里 :36.7 那一格节点延迟次高）。
// 首轮错开、每轮再抖一下，相位就不会固化下来。
type loopPacer struct {
	every time.Duration
	timer *time.Timer
	armed bool
}

// firstDelayCap 是首轮延迟的上限：间隔更短的循环取它自己的间隔。重启后最多
// 一分钟就会跑第一轮，配额滚动、定时公告这类到点即该生效的事不会被拖太久。
const firstDelayCap = time.Minute

func newLoopPacer(every time.Duration) *loopPacer {
	limit := min(every, firstDelayCap)
	return &loopPacer{every: every, timer: time.NewTimer(randDuration(limit))}
}

// C 返回下一轮的到点通道。第一次调用是首轮（随机延迟），之后每次调用都以
// every ±10% 重新定时；调用方在上一轮跑完之后再调它。
func (p *loopPacer) C() <-chan time.Time {
	if p.armed {
		p.timer.Reset(jittered(p.every))
	}
	p.armed = true
	return p.timer.C
}

func (p *loopPacer) Stop() { p.timer.Stop() }

// jittered 返回 every 加上 [-10%, +10%) 的均匀抖动。
func jittered(every time.Duration) time.Duration {
	span := every / 5
	if span <= 0 {
		return every
	}
	return every - every/10 + randDuration(span)
}

func randDuration(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(limit)))
}
