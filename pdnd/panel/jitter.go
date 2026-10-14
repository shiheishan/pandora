package panel

import (
	"math/rand/v2"
	"time"
)

// Jitter 把一个节拍随机放大或缩小至多 10%。
//
// 200 个节点同一批装好、同一时刻启动，节拍一致的话每 15 秒就在同一秒里一起打
// 面板，平均负载不高、尖峰却是平均的几十倍。每轮各自抖一点，几轮之后就散开了。
func Jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	span := int64(d) / 5 // 总幅度 20%，即 ±10%
	if span <= 0 {
		return d
	}
	return d - time.Duration(span/2) + time.Duration(rand.Int64N(span+1))
}
