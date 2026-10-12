package app

import (
	"testing"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 生产装配必须给 commission 注入同源判断：漏了的话每条佣金都会转人工复核（失败即关），这里提前红。
func TestCommissionWiredWithSourceMatcher(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })
	r := &runtime{cfg: testConfig(t), log: zap.NewNop(), redis: rdb}
	svc, err := buildServices(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.close)
	if !svc.commission.SourceMatcherWired() {
		t.Fatal("commission 没有注入同源判断（UseSourceMatcher）")
	}
}
