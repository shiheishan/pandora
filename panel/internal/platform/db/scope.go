package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 注入租户上下文的语句。两个参数：租户、操作者（可为空串）。
const scopeSetConfigSQL = `SELECT set_config('app.tenant_id', $1, true),
		        set_config('app.actor_id',  $2, true)`

// errMissingTenant 是 DATA-002 的入口闸：没有租户就不发任何语句。
var errMissingTenant = errors.New("db: 缺少 tenant_id，拒绝执行（DATA-002）")

// scopedReleaseKey 是受控路径归还连接前打的标记（存在连接自带的 CustomData 里），
// afterRelease 见到它就跳过会话清理。见 OpenWithOptions 的说明。
const scopedReleaseKey = "aegis.db.scoped_release"

// sessionResetSQL 是不受控路径归还时的会话清理（会话级，故意为之）。
const sessionResetSQL = `SELECT set_config('app.tenant_id', '', false),
		                              set_config('app.actor_id', '', false)`

// afterRelease 是连接池的归还钩子：受控路径用过的连接直接回池；其余连接清掉
// 会话级的租户上下文再回池，清理失败就销毁。pgxpool 在归还后的协程里调用它，
// 不在请求的关键路径上，但这条连接在清理完成之前不可用、库里多执行一条语句。
func afterRelease(c *pgx.Conn) bool {
	data := c.PgConn().CustomData()
	if data[scopedReleaseKey] == true {
		delete(data, scopedReleaseKey)
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Exec(ctx, sessionResetSQL)
	return err == nil
}

// releaseScoped 归还受控路径用过的连接：回到空闲状态的才打标记，其余交给
// pgxpool 销毁（它对非空闲连接一律销毁，不会调用 afterRelease）。
func releaseScoped(c *pgxpool.Conn) {
	if pc := c.Conn().PgConn(); !pc.IsClosed() && !pc.IsBusy() && pc.TxStatus() == 'I' {
		pc.CustomData()[scopedReleaseKey] = true
	}
	c.Release()
}

// runScoped 是 InTx 与 InTxSerializable 的共同实现。
//
// 开事务与注入租户上下文合成一次往返：BEGIN 与 set_config 作为同一条简单协议
// 多语句发出（见 scopedBeginSQL）。以前是 BEGIN 一次、set_config 一次，
// 每个事务固定多一次往返；三个网关的每个已登录请求至少一个事务，这一次往返
// 乘上全部请求就是库前的地板开销。
//
// 只有租户与操作者都是规范小写 UUID（或操作者为空）时才内联进 SQL：字面量
// 只含十六进制与连字符，没有引号可逃逸。其余情况退回参数化的两次往返，
// 行为与以前完全一致。
func (p *Pool) runScoped(ctx context.Context, s Scope, opts pgx.TxOptions, beginLabel string,
	wrapCommit bool, fn func(pgx.Tx) error) error {
	if s.TenantID == "" {
		return errMissingTenant
	}

	inlined := false
	if begin, ok := scopedBeginSQL(s, opts.IsoLevel); ok {
		opts.BeginQuery = begin
		inlined = true
	}
	conn, err := p.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", beginLabel, err)
	}
	// 先回滚（事务已提交时是空操作）、再归还：defer 后进先出
	defer releaseScoped(conn)
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("%s: %w", beginLabel, err)
	}
	defer rollbackForCleanup(tx)

	if !inlined {
		if _, err := tx.Exec(ctx, scopeSetConfigSQL, s.TenantID, s.ActorID); err != nil {
			return fmt.Errorf("注入租户上下文: %w", err)
		}
	}

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		if wrapCommit {
			return fmt.Errorf("提交事务: %w", err)
		}
		return err
	}
	return nil
}

// scopedBeginSQL 返回「BEGIN + 注入租户上下文」的单次往返写法；ok=false 表示
// 本次不能内联（值不是规范 UUID），调用方退回参数化路径。
//
// 只支持 InTx 用到的两种隔离级别：缺省（读已提交）与可串行化。
func scopedBeginSQL(s Scope, iso pgx.TxIsoLevel) (string, bool) {
	if !canonicalUUID(s.TenantID) || (s.ActorID != "" && !canonicalUUID(s.ActorID)) {
		return "", false
	}
	var b strings.Builder
	switch iso {
	case "":
		b.WriteString("BEGIN")
	case pgx.Serializable:
		b.WriteString("BEGIN ISOLATION LEVEL SERIALIZABLE")
	default:
		return "", false
	}
	// set_config 第三个参数为 true：事务级，随提交或回滚失效
	b.WriteString("; SELECT set_config('app.tenant_id', '")
	b.WriteString(s.TenantID)
	b.WriteString("', true), set_config('app.actor_id', '")
	b.WriteString(s.ActorID)
	b.WriteString("', true)")
	return b.String(), true
}

// canonicalUUID 只认 uuid 库输出的那一种写法（36 位小写带连字符）。
// uuid.Validate 还接受花括号、urn 前缀与 32 位无连字符写法，这里一律不内联。
func canonicalUUID(v string) bool {
	u, err := uuid.Parse(v)
	return err == nil && u.String() == v
}

// QueryRowScoped 在一次网络往返内注入租户上下文并执行一条单行语句，结果扫进 dest。
//
// 做法是 pgx 批次：set_config 与这条语句排进同一个管线，末尾只有一个 Sync。
// PostgreSQL 把同一个 Sync 之前的语句当作一个隐式事务执行，所以：
//   - set_config(..., true) 对后一条语句生效，Sync 之后随事务结束失效；
//   - 语句里带写入（如 CTE 里的 UPDATE）时，与读取一起在 Sync 处原子提交，
//     任一条失败整批回滚。
//
// 适用于热路径上「一条语句就能取齐」的读（认证中间件、降级开关）。需要多条语句、
// 中间要按结果分支的，仍用 InTx。查无此行时返回 pgx.ErrNoRows（可用 errors.Is 判断）。
//
// 首次在某条连接上执行某条 SQL 时 pgx 会先单独做一次准备（多一次往返），之后
// 走语句缓存，稳态是一次往返。
func (p *Pool) QueryRowScoped(ctx context.Context, s Scope, sql string, args []any, dest ...any) error {
	if s.TenantID == "" {
		return errMissingTenant
	}
	b := &pgx.Batch{}
	b.Queue(scopeSetConfigSQL, s.TenantID, s.ActorID)
	b.Queue(sql, args...).QueryRow(func(row pgx.Row) error { return row.Scan(dest...) })
	conn, err := p.Acquire(ctx)
	if err != nil {
		return err
	}
	defer releaseScoped(conn)
	return conn.SendBatch(ctx, b).Close()
}
