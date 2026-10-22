// Package testkit 是 repo / service 测试用的 PostgreSQL 与 Redis 夹具，只被 *_test.go 引用。
//
// PG 用例从 PANDORA_TEST_DATABASE_URL 取库，为空就跳过（跳过不等于通过，本机起 PG 见 verify skill「本机 PG」）。
// 每个测试进程（一个包）用自己的库：第一次调用 DB 时从模板库复制一份，所以各包可以并行跑（go test -p N）。
// 模板按迁移文件内容的摘要命名，第一个用到它的进程在咨询锁下建好并迁移，之后的进程直接复制。
// DSN 指的那个库只当维护库用（建库、删库、咨询锁），迁移往返测试（db/migrations）与 tx 包的测试直接用它。
package testkit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/aegispanel/aegis/db/migrations"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// DSNEnv 是测试库 DSN 的环境变量。
const DSNEnv = "PANDORA_TEST_DATABASE_URL"

// lockKey 是建模板、复制、清理时持有的咨询锁（任意固定值，只在测试库服务器上有意义）。
const lockKey = 0x70616e646f7261

var (
	prepareOnce sync.Once
	procURL     string
	prepareErr  error
)

// DB 打开本测试进程专用的库（已迁移到最新）；没有 DSN 时跳过测试。
func DB(t testing.TB) *db.DB {
	t.Helper()
	dsn := os.Getenv(DSNEnv)
	if dsn == "" {
		t.Skip(DSNEnv + " 未设置")
	}
	prepareOnce.Do(func() { procURL, prepareErr = prepareDB(dsn) })
	if prepareErr != nil {
		t.Fatalf("准备测试库: %v", prepareErr)
	}
	d, err := db.Open(context.Background(), db.Options{URL: procURL, MaxConns: 8, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// prepareDB 在 DSN 所在的服务器上为本进程建库 <库名>_run_<pid>_<随机>，返回它的 DSN。
// 顺带删掉进程已不在的 _run_ 库与迁移已变的旧模板，库不会越积越多。
func prepareDB(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("解析 DSN: %w", err)
	}
	base := strings.TrimPrefix(u.Path, "/")
	if base == "" {
		return "", errors.New("DSN 里没有库名")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		return "", err
	}
	defer admin.Close()
	conn, err := admin.Conn(ctx) // 咨询锁是会话级的，加锁与解锁要在同一条连接上
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return "", err
	}
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockKey) }()

	digest := migrationsDigest()
	tpl := base + "_tpl_" + digest
	sweep(ctx, conn, base, tpl)
	run := fmt.Sprintf("%s_run_%d_%s", base, os.Getpid(), randomHex(4))
	if digest == "" {
		// 找不到迁移源文件（比如 -trimpath 构建）：不用模板，直接迁移本进程的库
		if err := createDB(ctx, conn, run, "template0"); err != nil {
			return "", err
		}
		return withDB(u, run), migrateUp(withDB(u, run))
	}
	var exists bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", tpl).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		if err := createDB(ctx, conn, tpl, "template0"); err != nil {
			return "", err
		}
		if err := migrateUp(withDB(u, tpl)); err != nil {
			_, _ = conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quote(tpl))
			return "", fmt.Errorf("迁移模板库: %w", err)
		}
	}
	if err := createDB(ctx, conn, run, tpl); err != nil {
		return "", err
	}
	return withDB(u, run), nil
}

// sweep 删掉进程已退出的 _run_ 库（各包的测试进程都在本机，按 pid 判断），以及不是当前迁移的模板。
// 删不掉（别的会话正在用）就留着，下次再说。
func sweep(ctx context.Context, conn *sql.Conn, base, keepTpl string) {
	names, err := listTestDBs(ctx, conn, base)
	if err != nil {
		return
	}
	for _, name := range names {
		q := ""
		if rest, ok := strings.CutPrefix(name, base+"_run_"); ok {
			pidText, _, _ := strings.Cut(rest, "_")
			if pid, err := strconv.Atoi(pidText); err == nil && !processAlive(pid) {
				// 进程已退出的库可能还挂着它没关的连接
				q = "DROP DATABASE IF EXISTS " + quote(name) + " WITH (FORCE)"
			}
		} else if name != keepTpl {
			q = "DROP DATABASE IF EXISTS " + quote(name)
		}
		if q != "" {
			_, _ = conn.ExecContext(ctx, q)
		}
	}
}

// listTestDBs 列出 base 名下的 _run_ 库与 _tpl_ 模板（读完就关掉结果集，同一条连接接着还要删库）。
func listTestDBs(ctx context.Context, conn *sql.Conn, base string) ([]string, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT datname FROM pg_database WHERE starts_with(datname, $1) OR starts_with(datname, $2)",
		base+"_run_", base+"_tpl_")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func createDB(ctx context.Context, conn *sql.Conn, name, template string) error {
	_, err := conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s ENCODING 'UTF8'", quote(name), quote(template)))
	if err != nil {
		return fmt.Errorf("建库 %s: %w", name, err)
	}
	return nil
}

// migrationsDigest 是 db/migrations 下全部 .sql 的文件名与内容的摘要（12 位十六进制），按源码位置找文件；找不到返回空串。
func migrationsDigest() string {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	fsys := os.DirFS(filepath.Join(filepath.Dir(self), "..", "..", "..", "db", "migrations"))
	files, err := fs.Glob(fsys, "*.sql") // 按文件名排好序
	if err != nil || len(files) == 0 {
		return ""
	}
	h := sha256.New()
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return ""
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func withDB(u *url.URL, name string) string {
	c := *u
	c.Path = "/" + name
	return c.String()
}

func quote(name string) string { return pgx.Identifier{name}.Sanitize() }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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

// SQLState 取错误链里 PG 约束违例的 SQLSTATE，取不到返回空串（service 包的测试不能 import pkg/db）。
func SQLState(err error) string {
	v, _ := db.AsViolation(err)
	return v.Code
}
