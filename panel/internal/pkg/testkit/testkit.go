// Package testkit 是 repo / service 测试用的 PostgreSQL 与 Redis 夹具，只被 *_test.go 引用。
//
// PG 用例从 PANDORA_TEST_DATABASE_URL 取库，为空就跳过（本机没有 PG，跳过不等于通过，以 CI 为准）。
// CI 按包串行（-p 1），所有包共用一个库；迁移在每个测试进程里执行一次。
package testkit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/db/migrations"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/alicebob/miniredis/v2"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// DSNEnv 是测试库 DSN 的环境变量。
const DSNEnv = "PANDORA_TEST_DATABASE_URL"

var (
	migrateOnce sync.Once
	migrateErr  error
)

// DB 打开测试库并确保迁移到最新；没有 DSN 时跳过测试。
func DB(t testing.TB) *db.DB {
	t.Helper()
	dsn := os.Getenv(DSNEnv)
	if dsn == "" {
		t.Skip(DSNEnv + " 未设置")
	}
	migrateOnce.Do(func() { migrateErr = migrateUp(dsn) })
	if migrateErr != nil {
		t.Fatalf("迁移测试库: %v", migrateErr)
	}
	d, err := db.Open(context.Background(), db.Options{URL: dsn, MaxConns: 8, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func migrateUp(dsn string) error {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return migrations.Up(context.Background(), sqlDB)
}

// TxCtx 开一个事务放进 ctx，测试结束回滚。service 的 tx.Manager.Do 会加入这个事务，
// 所以用例写下的数据都不会留下；提交后钩子（AfterCommit）不会执行。
func TxCtx(t testing.TB, d *db.DB) context.Context {
	t.Helper()
	g := d.Base().Begin()
	if g.Error != nil {
		t.Fatalf("开事务: %v", g.Error)
	}
	t.Cleanup(func() { g.Rollback() })
	return db.WithTxState(context.Background(), db.NewTxState(g))
}

// Truncate 清空表（RESTART IDENTITY CASCADE），给需要真实提交的用例（并发、提交后钩子）用。
// 只追加的表连 TRUNCATE 也挡，这里在事务内临时 session_replication_role = replica 跳过守卫
// （测试库用超级用户连，产品代码不这么做）。
func Truncate(t testing.TB, d *db.DB, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		return
	}
	q := fmt.Sprintf("TRUNCATE %s RESTART IDENTITY CASCADE", strings.Join(tables, ", "))
	err := d.Base().Transaction(func(g *gorm.DB) error {
		if err := g.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
			return err
		}
		return g.Exec(q).Error
	})
	if err != nil {
		t.Fatalf("清表: %v", err)
	}
}

// Redis 起一个 miniredis 并返回连上它的客户端。
func Redis(t testing.TB) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = c.Close() })
	return mr, c
}
