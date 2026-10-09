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

// intervalGate 让一个更慢的周期挂在更快的节拍上：每个节拍问一次 due，隔够了 every 才返回 true。
//
// 保留期清理原先和按天汇总一起每 10 分钟跑一轮；汇总要及时（日界后 10 分钟内），清理一小时一次
// 足够（过期的数据在读路径上都有时间窗，清得再勤只是空转）。两者共用一个循环、一个节拍，清理
// 挂在这个闸门后面。节拍有 ±10% 抖动，所以离整点最近的那一拍就算到点（elapsed + tick/2 >= every），
// 不会因为差几十秒而再等一拍。
type intervalGate struct {
	every, tick time.Duration
	last        time.Time
}

func newIntervalGate(every, tick time.Duration) *intervalGate {
	return &intervalGate{every: every, tick: tick}
}

// due 报告这一拍该不该跑；还没跑过（刚启动）时一定该跑。
func (g *intervalGate) due(now time.Time) bool {
	return g.last.IsZero() || now.Sub(g.last)+g.tick/2 >= g.every
}

// done 记下一次成功的运行。没有成功就不调：下一拍再试，不必等满一个间隔。
func (g *intervalGate) done(now time.Time) { g.last = now }
