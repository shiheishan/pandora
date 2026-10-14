package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// reportCacheTTL 是后台看板短缓存时长（30 秒）。
const reportCacheTTL = 30 * time.Second

// UseCache 为 report 模块注入 Redis 客户端，启用看板短缓存。
func (s *Service) UseCache(rdb *redis.Client) {
	s.rdb = rdb
}

// cached 封装 Redis 30 秒只读短缓存；s.rdb 为 nil 或 Redis 出错时不阻断业务，降级为直查。
func cached[T any](ctx context.Context, s *Service, key string, load func(context.Context) (T, error)) (T, error) {
	if s == nil || s.rdb == nil {
		return load(ctx)
	}

	raw, err := s.rdb.Get(ctx, key).Bytes()
	if err == nil {
		var val T
		if err := json.Unmarshal(raw, &val); err == nil {
			return val, nil
		}
	}

	val, err := load(ctx)
	if err != nil {
		return val, err
	}

	if data, mErr := json.Marshal(val); mErr == nil {
		_ = s.rdb.Set(ctx, key, data, reportCacheTTL).Err()
	}

	return val, nil
}
