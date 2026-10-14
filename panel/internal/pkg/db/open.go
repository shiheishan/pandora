package db

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Options 打开 PostgreSQL 连接时的参数。
type Options struct {
	URL string
	// MaxConns 是连接池上限（至少 1）；空闲连接也最多留这么多条，闲置超过 connMaxIdleTime 才关
	MaxConns int
	Logger   *zap.Logger
}

// slowSQLThreshold 是慢 SQL 的阈值：超过它的语句按 WARN 记进日志。
const slowSQLThreshold = 200 * time.Millisecond

// connMaxIdleTime 是空闲连接留在池里的最长时间：突发过后多开的连接闲置这么久才关。
const connMaxIdleTime = 5 * time.Minute

// Open 打开 GORM 连接并包装为 *DB。
func Open(ctx context.Context, o Options) (*DB, error) {
	if o.MaxConns < 1 {
		return nil, fmt.Errorf("db: MaxConns 至少为 1，当前 %d", o.MaxConns)
	}
	g, err := gorm.Open(postgres.New(postgres.Config{DSN: o.URL}), &gorm.Config{
		TranslateError:         true,
		SkipDefaultTransaction: true,
		PrepareStmt:            false,
		NowFunc:                func() time.Time { return time.Now().UTC() },
		Logger:                 newGormLogger(o.Logger, slowSQLThreshold),
	})
	if err != nil {
		return nil, err
	}

	if err := registerStatsCallbacks(g); err != nil {
		return nil, fmt.Errorf("注册 SQL 计数回调: %w", err)
	}

	sqlDB, err := g.DB()
	if err != nil {
		return nil, err
	}

	// 空闲上限与池上限相同，靠 connMaxIdleTime 回收：空闲上限小于并发时，每次归还都会关掉多出来的连接、
	// 下一次再新建（P8 实测空闲上限 2、池 30 时，空载每分钟约 17 个新连接，压测时每秒 9–15 个）。
	sqlDB.SetMaxOpenConns(o.MaxConns)
	sqlDB.SetMaxIdleConns(o.MaxConns)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(connMaxIdleTime)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return Wrap(g), nil
}

// Close 关闭底层连接池。
func (d *DB) Close() error {
	sqlDB, err := d.base.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping 检查数据库是否可达。
func (d *DB) Ping(ctx context.Context) error {
	sqlDB, err := d.base.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// runtimeRoleQuery 一次取齐运行角色的安全相关属性（docs/architecture.md「运行角色」一行）。
const runtimeRoleQuery = `SELECT current_user, session_user,
       cr.rolsuper, cr.rolcreaterole, cr.rolcreatedb, sr.rolsuper,
       pg_catalog.has_database_privilege(current_user, pg_catalog.current_database(), 'TEMPORARY'),
       pg_catalog.has_database_privilege(current_user, pg_catalog.current_database(), 'CREATE'),
       pg_catalog.has_schema_privilege(current_user, 'public', 'CREATE'),
       EXISTS (SELECT 1 FROM pg_catalog.pg_class c
                JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
               WHERE c.relowner = cr.oid AND n.nspname NOT IN ('pg_catalog', 'information_schema')),
       pg_catalog.current_setting('search_path')
FROM pg_catalog.pg_roles AS cr
JOIN pg_catalog.pg_roles AS sr ON sr.rolname = session_user
WHERE cr.rolname = current_user`

// RequiredSearchPath 是 configure-app-role.sql 给运行角色按库钉住的 search_path。
const RequiredSearchPath = "pg_catalog, public, pg_temp"

// runtimeRole 是 runtimeRoleQuery 的一行结果。
type runtimeRole struct {
	current, session               string
	super, createRole, createDB    bool
	sessionSuper                   bool
	dbTemp, dbCreate, publicCreate bool
	ownsRelations                  bool
	searchPath                     string
}

// CheckRuntimeRole 确认当前库角色是收窄过的运行角色（仅 production 启动时调用）：
// 不是超级用户、不能建角色建库、没有 TEMP 与 CREATE、不拥有任何表，search_path 固定。
func (d *DB) CheckRuntimeRole(ctx context.Context) error {
	var r runtimeRole
	err := d.Conn(ctx).Raw(runtimeRoleQuery).Row().Scan(
		&r.current, &r.session,
		&r.super, &r.createRole, &r.createDB, &r.sessionSuper,
		&r.dbTemp, &r.dbCreate, &r.publicCreate,
		&r.ownsRelations, &r.searchPath,
	)
	if err != nil {
		return fmt.Errorf("查询运行角色权限: %w", err)
	}
	return r.validate()
}

func (r runtimeRole) validate() error {
	switch {
	case r.current != r.session:
		return fmt.Errorf("运行角色 %q 与会话角色 %q 不一致", r.current, r.session)
	case r.super || r.sessionSuper:
		return fmt.Errorf("运行角色 %q 不能是超级用户", r.current)
	case r.createRole || r.createDB:
		return fmt.Errorf("运行角色 %q 不能有建角色、建库权限", r.current)
	case r.dbTemp:
		return fmt.Errorf("运行角色 %q 不能有建临时表（TEMPORARY）权限", r.current)
	case r.dbCreate || r.publicCreate:
		return fmt.Errorf("运行角色 %q 不能有建 schema 或在 public 建对象的权限", r.current)
	case r.ownsRelations:
		return fmt.Errorf("运行角色 %q 不能是任何表的属主", r.current)
	case r.searchPath != RequiredSearchPath:
		return fmt.Errorf("运行角色 %q 的 search_path 是 %q，应为 %q", r.current, r.searchPath, RequiredSearchPath)
	}
	return nil
}
