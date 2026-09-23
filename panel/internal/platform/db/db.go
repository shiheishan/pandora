// Package db 封装 PostgreSQL 连接池与租户上下文。
//
// 本包只做一件不可妥协的事：任何业务查询都必须在设置了 app.tenant_id 的会话里执行。
// 忘记设置时，行级安全策略会让所有受保护的表返回空集 —— 这是 DATA-002 要求的
// 「查询遗漏租户条件时由防护层阻断」。
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Pool struct {
	*pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串: %w", err)
	}

	// 1 vCPU / 2G 内存的宿主，且 PG 侧 max_connections=60。
	// 四个网关进程 + worker 共享，单进程留 8 条足够，避免连接耗尽。
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	// 归还连接前清掉会话级 GUC，杜绝租户上下文串到下一个使用者身上。
	cfg.AfterRelease = func(c *pgx.Conn) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := c.Exec(ctx, `SELECT set_config('app.tenant_id', '', false),
		                              set_config('app.actor_id', '', false)`)
		return err == nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("建立连接池: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库不可达: %w", err)
	}
	if err := validateRuntimeRole(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Pool{pool}, nil
}

// queryRower is deliberately smaller than pgxpool.Pool so the security check
// can be unit-tested without a live PostgreSQL instance.
type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// validateRuntimeRole refuses database roles that can bypass tenant RLS.
// FORCE ROW LEVEL SECURITY does not constrain superusers or BYPASSRLS roles,
// and row_security=off can turn an accidental policy miss into an error only
// when the caller is otherwise subject to RLS. All three checks are therefore
// required and fail closed before the pool is returned to the application.
const runtimeRoleSecurityQuery = `SELECT current_user, session_user,
       cr.rolsuper, cr.rolbypassrls,
       sr.rolsuper, sr.rolbypassrls,
       current_setting('row_security'),
       pg_catalog.has_database_privilege(current_user, current_database(), 'TEMPORARY'),
       pg_catalog.has_schema_privilege(current_user, 'public', 'CREATE'),
       pg_catalog.has_schema_privilege(current_user, 'app', 'CREATE'),
       current_setting('search_path')
FROM pg_catalog.pg_roles AS cr
JOIN pg_catalog.pg_roles AS sr ON sr.rolname = session_user
WHERE cr.rolname = current_user`

func validateRuntimeRole(ctx context.Context, q queryRower) error {

	var currentRole, sessionRole, rowSecurity, searchPath string
	var currentSuperuser, currentBypassRLS, sessionSuperuser, sessionBypassRLS bool
	var databaseTemporary, publicCreate, appCreate bool
	if err := q.QueryRow(ctx, runtimeRoleSecurityQuery).Scan(
		&currentRole, &sessionRole,
		&currentSuperuser, &currentBypassRLS,
		&sessionSuperuser, &sessionBypassRLS,
		&rowSecurity,
		&databaseTemporary, &publicCreate, &appCreate,
		&searchPath,
	); err != nil {
		return fmt.Errorf("db: cannot verify runtime role security: %w", err)
	}
	if currentRole != sessionRole {
		return fmt.Errorf("db: unsafe role switch: current_user %q differs from session_user %q", currentRole, sessionRole)
	}
	if currentSuperuser || sessionSuperuser {
		return fmt.Errorf("db: unsafe runtime/session role %q is a PostgreSQL superuser", currentRole)
	}
	if currentBypassRLS || sessionBypassRLS {
		return fmt.Errorf("db: unsafe runtime/session role %q has BYPASSRLS", currentRole)
	}
	if rowSecurity != "on" {
		return fmt.Errorf("db: unsafe row_security setting %q for runtime role %q; expected on", rowSecurity, currentRole)
	}
	if databaseTemporary {
		return fmt.Errorf("db: unsafe runtime role %q has database TEMPORARY privilege", currentRole)
	}
	if publicCreate {
		return fmt.Errorf("db: unsafe runtime role %q has CREATE on schema public", currentRole)
	}
	if appCreate {
		return fmt.Errorf("db: unsafe runtime role %q has CREATE on schema app", currentRole)
	}
	const requiredSearchPath = "pg_catalog, public, pg_temp"
	if searchPath != requiredSearchPath {
		return fmt.Errorf("db: unsafe search_path %q for runtime role %q; expected %q", searchPath, currentRole, requiredSearchPath)
	}
	return nil
}

// Scope 是一次带租户上下文的数据库操作范围。
type Scope struct {
	TenantID string
	ActorID  string // 可为空（匿名或系统操作）
}

type transactionRollbacker interface {
	Rollback(context.Context) error
}

// rollbackForCleanup must not inherit a canceled request context. A canceled
// context makes pgx close the connection when it cannot send ROLLBACK; a short,
// independent cleanup context gives PostgreSQL a chance to release locks and
// lets the pool safely reuse the connection without allowing cleanup to hang.
func rollbackForCleanup(tx transactionRollbacker) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// InTx 在事务中执行 fn，并在事务开始时注入租户上下文。
//
// 用 SET LOCAL 而非 SET：GUC 随事务结束自动失效，
// 即使连接被复用也不会带着上一个租户的身份。
func (p *Pool) InTx(ctx context.Context, s Scope, fn func(pgx.Tx) error) error {
	if s.TenantID == "" {
		return errors.New("db: 缺少 tenant_id，拒绝执行（DATA-002）")
	}

	tx, err := p.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer rollbackForCleanup(tx)

	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1, true),
		        set_config('app.actor_id',  $2, true)`,
		s.TenantID, s.ActorID); err != nil {
		return fmt.Errorf("注入租户上下文: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交事务: %w", err)
	}
	return nil
}

// InTxSerializableRetry 是 InTxSerializable 加上对 40001 的自动重试。
//
// 序列化失败是 SERIALIZABLE 下的正常产物，不是故障：数据库发现两个事务
// 没法串行化，回滚掉其中一个。回滚是干净的，重试通常立刻成功。把它当错误
// 抛给用户（「请求冲突，请重试」）等于让用户替数据库做退避——50 并发压测
// 下有近三成下单撞上这个。
//
// 要求 fn 除了写数据库不做别的：重试会完整重放它。带外部副作用（发消息、
// 调第三方）的逻辑不能用这个入口。
func (p *Pool) InTxSerializableRetry(ctx context.Context, s Scope, fn func(pgx.Tx) error) error {
	const attempts = 4
	var err error
	for i := 0; i < attempts; i++ {
		err = p.InTxSerializable(ctx, s, fn)
		if err == nil || !IsSerializationFailure(err) {
			return err
		}
		if ctx.Err() != nil {
			return err
		}
		// 退避一点点再试，避免两个事务反复以同样的节奏互相撞。
		// 量级是毫秒：序列化冲突的窗口本来就很短。
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(2<<i) * time.Millisecond):
		}
	}
	return err
}

// InTxSerializable 用于必须防写偏斜的场景：库存扣减、配额扣减、优惠券兑换。
// 需要自动消化 40001 的场景请用 InTxSerializableRetry。
func (p *Pool) InTxSerializable(ctx context.Context, s Scope, fn func(pgx.Tx) error) error {
	if s.TenantID == "" {
		return errors.New("db: 缺少 tenant_id，拒绝执行（DATA-002）")
	}

	tx, err := p.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("开启序列化事务: %w", err)
	}
	defer rollbackForCleanup(tx)

	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1, true),
		        set_config('app.actor_id',  $2, true)`,
		s.TenantID, s.ActorID); err != nil {
		return fmt.Errorf("注入租户上下文: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- 错误分类：让上层不必到处写 pgconn 类型断言 ---

func pgErr(err error) *pgconn.PgError {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}

// IsUniqueViolation 报告是否为唯一约束冲突（23505）。
// 幂等入口靠它把「重复提交」翻译成「返回已有结果」而不是 500。
func IsUniqueViolation(err error) bool {
	pe := pgErr(err)
	return pe != nil && pe.Code == "23505"
}

func IsForeignKeyViolation(err error) bool {
	pe := pgErr(err)
	return pe != nil && pe.Code == "23503"
}

// ConstraintName 返回冲突的约束名，便于精确定位是哪条业务规则被触发。
func ConstraintName(err error) string {
	if pe := pgErr(err); pe != nil {
		return pe.ConstraintName
	}
	return ""
}

// IsCheckViolation 报告是否为 CHECK 约束或状态机守卫触发（23514）。
func IsCheckViolation(err error) bool {
	pe := pgErr(err)
	return pe != nil && pe.Code == "23514"
}

// IsInsufficientPrivilege 报告是否撞上了追加写保护或自我审批守卫（42501）。
func IsInsufficientPrivilege(err error) bool {
	pe := pgErr(err)
	return pe != nil && pe.Code == "42501"
}

// IsSerializationFailure 报告事务是否因序列化冲突失败（40001），调用方可安全重试。
func IsSerializationFailure(err error) bool {
	pe := pgErr(err)
	return pe != nil && (pe.Code == "40001" || pe.Code == "40P01")
}

// Message 返回数据库抛出的业务消息。
// 迁移里的 RAISE EXCEPTION 写的是中文可读文案，直接透给内部日志很有用；
// 但绝不能原样返回给公网 —— 那会泄露表名与约束名（SEC-006）。
func Message(err error) string {
	if pe := pgErr(err); pe != nil {
		return pe.Message
	}
	return err.Error()
}
