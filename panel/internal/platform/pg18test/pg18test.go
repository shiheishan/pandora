// [INPUT]: 依赖 platform/db 的 Open（运行时角色连接池）、pgxpool 的管理连接，依赖 run-pg18-gates.sh 按域注入的 AEGIS_<域>_PG18_* 环境变量
// [OUTPUT]: 对外提供 Fixture、Open
// [POS]: platform 的测试辅助包：PG18 集成测试打开一次性库的统一护栏，只被 *_pg18_test.go 引用，不进任何生产二进制
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package pg18test 打开 run-pg18-gates.sh 为某个域准备的一次性 PostgreSQL 18 库。
//
// 每个域都要证明自己连的是「这一次、这一个」一次性库，而不是随便一个真库：
// 库名前缀、标记表里的 run ID、库注释里的 run ID 三者都对上才放行，admin
// 与 app 两条连接各验一遍。这套护栏以前在每个 *_pg18_test.go 里各抄一份，
// 新用例统一走这里；环境变量一个都没有时跳过（本机没有库），给了一部分则
// 失败（接线接错了，跳过会让门禁在绿灯下什么也没验）。
package pg18test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// Fixture 描述一个域的一次性库身份，取值与 run-pg18-gates.sh 的域定义一一对应。
type Fixture struct {
	// Domain 是环境变量里的域名段，如 SUPPORT → AEGIS_SUPPORT_PG18_*
	Domain string
	// DatabasePrefix 是库名必须带的前缀
	DatabasePrefix string
	// MarkerTable 是装着 run_id 的标记表
	MarkerTable string
	// CommentTag 是库注释的前缀，注释须为 CommentTag + ":" + runID
	CommentTag string
}

var runIDPattern = regexp.MustCompile(`^[a-z0-9]{32}$`)

// Open 校验并打开一次性库，返回带超时的 ctx、管理连接（用于造数据与核对）与
// 运行时角色 aegis_app 的连接池（被测代码走它，RLS 与列级授权都是真的）。
// 三者的释放都挂在 t.Cleanup 上。
func Open(t testing.TB, f Fixture) (context.Context, *pgxpool.Pool, *db.Pool) {
	t.Helper()
	env := func(k string) string {
		return strings.TrimSpace(os.Getenv("AEGIS_" + f.Domain + "_PG18_" + k))
	}
	fixture, appDSN, adminDSN, database, runID :=
		env("FIXTURE"), env("DSN"), env("ADMIN_DSN"), env("DATABASE"), env("RUN_ID")
	if fixture == "" && appDSN == "" && adminDSN == "" && database == "" && runID == "" {
		t.Skip(strings.ToLower(f.Domain) + " PostgreSQL 18 fixture is not configured")
	}
	if fixture != "disposable-v1" || appDSN == "" || adminDSN == "" || database == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(database, f.DatabasePrefix) || !runIDPattern.MatchString(runID) {
		t.Fatalf("refusing malformed disposable identity database=%q run_id=%q", database, runID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture administrator pool: %v", err)
	}
	t.Cleanup(admin.Close)
	app, err := db.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open real aegis_app pool: %v", err)
	}
	t.Cleanup(app.Close)

	identify := `SELECT current_database(), current_setting('server_version_num')::int,
		(SELECT run_id FROM public.` + f.MarkerTable + `), shobj_description(oid,'pg_database')
		FROM pg_database WHERE datname=current_database()`
	wantComment := f.CommentTag + ":" + runID
	check := func(who string, scan func(dest ...any) error) {
		var got, marker, comment string
		var version int
		if err := scan(&got, &version, &marker, &comment); err != nil {
			t.Fatalf("identify %s PostgreSQL fixture: %v", who, err)
		}
		if got != database || version/10000 != 18 || marker != runID || comment != wantComment {
			t.Fatalf("refusing unexpected %s fixture database=%q version=%d marker=%q comment=%q",
				who, got, version, marker, comment)
		}
	}
	check("administrator", admin.QueryRow(ctx, identify).Scan)
	check("application", app.QueryRow(ctx, identify).Scan)
	return ctx, admin, app
}
