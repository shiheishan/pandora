package nodesim

import (
	"math/rand/v2"
	"time"
)

// 节拍与抖动。
//
// current 照新 pdnd（pdnd/node/node.go Run、pdnd/panel/jitter.go）：拉取、上报、心跳
// 三条节拍都是定时器，每拍处理完才按 ±10% 抖动重排下一拍，所以实际周期是
// 「处理耗时 + 抖动后的间隔」；换钥检查的间隔同样抖动。
// legacy 照老 pdnd：固定周期的 ticker，处理慢了就丢拍，没有抖动。
//
// pdnd 用全局随机源；模拟器给每个节点的每条节拍各配一个由 -seed 派生的独立随机源，
// 同一种子下每条节拍的间隔序列与请求快慢、事件到达次序无关，可以逐条复现。

// jitterSalt 把抖动随机源与节点主随机源（PCG(seed, i+1)）、起跑错开（PCG(seed, 0)）分开。
const jitterSalt = 0x6a6974746572 // "jitter"

// jitterSource 是一个模拟节点四条节拍各自的随机源。
type jitterSource struct {
	pull, push, status, key *rand.Rand
}

func newJitterSource(seed uint64, index int) jitterSource {
	stream := func(k uint64) *rand.Rand {
		return rand.New(rand.NewPCG(seed^jitterSalt, uint64(index)<<2|k))
	}
	return jitterSource{pull: stream(0), push: stream(1), status: stream(2), key: stream(3)}
}

// jitter 与 pdnd panel.Jitter 同口径：在 d 上下各抖至多 10%（总幅度 20%），
// 只是随机源由调用方给。
func jitter(rng *rand.Rand, d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	span := int64(d) / 5
	if span <= 0 {
		return d
	}
	return d - time.Duration(span/2) + time.Duration(rng.Int64N(span+1))
}

// beat 是主循环里的一条节拍：legacy 用 ticker，current 用带抖动的定时器。
type beat struct {
	ticker *time.Ticker
	timer  *time.Timer
	rng    *rand.Rand
}

func newBeat(d time.Duration, legacy bool, rng *rand.Rand) *beat {
	if legacy {
		return &beat{ticker: time.NewTicker(d)}
	}
	return &beat{timer: time.NewTimer(jitter(rng, d)), rng: rng}
}

func (b *beat) C() <-chan time.Time {
	if b.ticker != nil {
		return b.ticker.C
	}
	return b.timer.C
}

// fired 在这一拍处理完之后调用：定时器按 d 抖动后重排（pdnd 在每个 case 末尾 Reset），
// ticker 自己会走，什么都不做。
func (b *beat) fired(d time.Duration) {
	if b.timer != nil {
		b.timer.Reset(jitter(b.rng, d))
	}
}

// reset 在面板经 base_config 改了节拍之后调用。
func (b *beat) reset(d time.Duration) {
	if b.ticker != nil {
		b.ticker.Reset(d)
		return
	}
	b.timer.Reset(jitter(b.rng, d))
}

func (b *beat) stop() {
	if b.ticker != nil {
		b.ticker.Stop()
		return
	}
	b.timer.Stop()
}
