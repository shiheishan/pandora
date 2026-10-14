package db_test

// 要库，本机没有 PG 时跳过，以 CI 为准。

import (
	"context"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// TestPoolKeepsIdleUpToMaxConns 钉住连接池的空闲上限：并发用满池之后，归还的连接都留在池里复用，
// 不会因为空闲上限小于池上限被关掉、下次再新建（P8 发现的反复建连）。
func TestPoolKeepsIdleUpToMaxConns(t *testing.T) {
	d := testkit.DB(t)
	ctx := context.Background()
	sqlDB, err := d.Conn(ctx).DB()
	if err != nil {
		t.Fatal(err)
	}
	n := sqlDB.Stats().MaxOpenConnections
	if n < 2 {
		t.Fatalf("测试库连接池上限 %d，至少要 2 才测得出", n)
	}

	start := make(chan struct{})
	errc := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// 每条都占住连接一会儿，逼连接池开满
			_, err := sqlDB.ExecContext(ctx, "SELECT pg_sleep(0.2)")
			errc <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}

	st := sqlDB.Stats()
	if st.MaxIdleClosed != 0 {
		t.Fatalf("有 %d 条连接因空闲上限被关掉", st.MaxIdleClosed)
	}
	if st.Idle != n {
		t.Fatalf("空闲连接 %d 条，期望 %d（打开 %d 条）", st.Idle, n, st.OpenConnections)
	}
}
