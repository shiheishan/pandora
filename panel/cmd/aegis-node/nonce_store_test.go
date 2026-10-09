package main

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

// Valkey 连得上却不应答（挂住）时，nonce 认领必须在守卫给的 250ms 期限附近返回，
// 而不是 go-redis 缺省的 3 秒读超时（对抗审查 w12nonce #2）。用真 go-redis 客户端对着
// 只接受连接、从不回包的 TCP 监听测。
func TestNonceStoreHonoursTimeoutAgainstHungValkey(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var held []net.Conn
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c) // 只收不回
			mu.Unlock()
		}
	}()

	rdb := redis.NewClient(nonceRedisOptions(&redis.Options{Addr: ln.Addr().String()}))
	defer rdb.Close()
	store := valkeyNonceStore{rdb: rdb}
	for i := 0; i < 2; i++ { // 第二次走已建好（同样挂住）的连接或重拨
		ctx, cancel := context.WithTimeout(context.Background(), nodefabric.NonceStoreTimeout)
		start := time.Now()
		_, err := store.ClaimNonce(ctx, "aegis:node-nonce:hung", time.Minute)
		took := time.Since(start)
		cancel()
		if err == nil {
			t.Fatal("hung Valkey reported a claim")
		}
		if took > 2*nodefabric.NonceStoreTimeout {
			t.Fatalf("attempt %d: claim against a hung Valkey took %s (err %v)", i, took, err)
		}
		t.Logf("attempt %d: took %s err %v", i, took, err)
	}
}

// 派生配置不改共用客户端的配置。
func TestNonceRedisOptionsLeaveSharedClientAlone(t *testing.T) {
	base := &redis.Options{Addr: "127.0.0.1:6380"}
	opt := nonceRedisOptions(base)
	if !opt.ContextTimeoutEnabled || opt.ReadTimeout != nodefabric.NonceStoreTimeout || opt.Addr != base.Addr {
		t.Fatalf("nonce options: %+v", opt)
	}
	if base.ContextTimeoutEnabled || base.ReadTimeout != 0 {
		t.Fatal("deriving nonce options mutated the shared client options")
	}
}
