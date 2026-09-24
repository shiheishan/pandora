// [INPUT]: 依赖 realtime.go 的 Hub（Count、ctx、rdb），依赖 go-redis 的 SET/SCAN/MGET/DEL
// [OUTPUT]: 对外提供 SSEConnections；包内提供 Hub.reportConnections
// [POS]: platform/realtime 的跨进程在线连接计数：每个网关进程定期把本机 SSE 连接数写进 Valkey 带 TTL 的键，后台系统状态求和（进程内 Count 只看得到本进程）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package realtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	sseCountKeyPrefix = "rt:sse:conns:"
	sseReportEvery    = 30 * time.Second
	// TTL 是上报周期的三倍：进程崩溃时它的计数最多残留 90 秒，而一次上报
	// 迟到不会让计数闪成 0
	sseReportTTL = 3 * sseReportEvery
)

// reportConnections 周期上报本进程的连接数，Hub 关闭时删掉自己的键。
func (h *Hub) reportConnections() {
	key := sseCountKeyPrefix + instanceID()
	write := func() {
		ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
		defer cancel()
		if err := h.rdb.Set(ctx, key, h.Count(), sseReportTTL).Err(); err != nil && h.ctx.Err() == nil {
			h.log.Warn("上报 SSE 连接数失败", "err", err)
		}
	}
	write()
	ticker := time.NewTicker(sseReportEvery)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = h.rdb.Del(ctx, key).Err()
			cancel()
			return
		case <-ticker.C:
			write()
		}
	}
}

// SSEConnections 汇总所有网关进程上报的在线 SSE 连接数。
func SSEConnections(ctx context.Context, rdb *redis.Client) (int, error) {
	if rdb == nil {
		return 0, fmt.Errorf("realtime: 没有 Valkey 连接")
	}
	total := 0
	iter := rdb.Scan(ctx, 0, sseCountKeyPrefix+"*", 100).Iterator()
	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	vals, err := rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, err
	}
	for _, v := range vals {
		// 两次调用之间过期的键回 nil，跳过
		if s, ok := v.(string); ok {
			if n, err := strconv.Atoi(s); err == nil {
				total += n
			}
		}
	}
	return total, nil
}

// instanceID 区分同一台机器上的多个网关进程与重启前后的同一进程。
func instanceID() string {
	host, _ := os.Hostname()
	var b [4]byte
	_, _ = rand.Read(b[:])
	return host + ":" + strconv.Itoa(os.Getpid()) + ":" + hex.EncodeToString(b[:])
}
