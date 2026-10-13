package panel

import "time"

// Backoff 是全抖动指数退避：第 n 次等待取 [0, min(Max, Base·2ⁿ)) 里的随机值。
// 一批节点同时断线（面板重启）时各自散开重连，不在同一秒打面板。
type Backoff struct {
	Base, Max time.Duration
	// Rand 返回 [0, n) 的随机数，测试可替换。
	Rand    func(n int64) int64
	attempt int
}

// Next 返回下一次重连前的等待，并把上限翻倍（封顶 Max）。
func (b *Backoff) Next() time.Duration {
	ceil := b.Ceiling()
	if b.Base<<b.attempt < b.Max {
		b.attempt++
	}
	if ceil <= 0 {
		return 0
	}
	return time.Duration(b.Rand(int64(ceil)))
}

// Ceiling 是下一次等待的上限。
func (b *Backoff) Ceiling() time.Duration {
	ceil := b.Base << b.attempt
	if ceil <= 0 || ceil > b.Max {
		return b.Max
	}
	return ceil
}

// Reset 在一条流健康地跑起来（收到 Welcome）之后调用，下次断线从 Base 起算。
func (b *Backoff) Reset() { b.attempt = 0 }
