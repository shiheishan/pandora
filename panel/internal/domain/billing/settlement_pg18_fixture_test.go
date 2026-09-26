// [INPUT]: 依赖 middleware 幂等声明与 platform/db 事务，依赖 settlement.go 的 HandlePaymentWebhook（并发回调）
// [OUTPUT]: 包内提供固定的租户、用户、套餐、价格、认领与订单 ID 常量（settlementPG18*）、幂等认领 settlementPG18Claim / ClaimFor / InsertClaim、并发与竞态回调 settlementPG18RunConcurrentWebhooks / RunWebhookRace、死锁判定、时间与事务工具，以及运行角色 ACL 与强制 RLS 的断言 settlementPG18AssertRuntimeACL
// [POS]: TestSettlementPG18 的夹具与护栏
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/middleware"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

const (
	settlementPG18Tenant         = "93000000-0000-7000-8000-000000000001"
	settlementPG18User           = "93000000-0000-7000-8000-000000000012"
	settlementPG18Referrer       = "93000000-0000-7000-8000-000000000013"
	settlementPG18CommissionUser = "93000000-0000-7000-8000-000000000014"
	settlementPG18PlanExt        = "93000000-0000-7000-8000-000000000033"
	settlementPG18PriceExt       = "93000000-0000-7000-8000-000000000043"
	settlementPG18PlanMix        = "93000000-0000-7000-8000-000000000034"
	settlementPG18PriceMix       = "93000000-0000-7000-8000-000000000044"
	settlementPG18PlanFault      = "93000000-0000-7000-8000-000000000035"
	settlementPG18PriceFault     = "93000000-0000-7000-8000-000000000045"
	settlementPG18PlanCapture    = "93000000-0000-7000-8000-000000000036"
	settlementPG18PriceCapture   = "93000000-0000-7000-8000-000000000046"
	settlementPG18PlanFulfil     = "93000000-0000-7000-8000-000000000037"
	settlementPG18PriceFulfil    = "93000000-0000-7000-8000-000000000047"
	settlementPG18PlanAudit      = "93000000-0000-7000-8000-000000000038"
	settlementPG18PriceAudit     = "93000000-0000-7000-8000-000000000048"
	settlementPG18PlanCorrupt    = "93000000-0000-7000-8000-000000000039"
	settlementPG18ClaimExt       = "93000000-0000-7000-8000-000000000301"
	settlementPG18ClaimMix       = "93000000-0000-7000-8000-000000000302"
	settlementPG18ClaimTopup     = "93000000-0000-7000-8000-000000000303"
	settlementPG18ClaimFault     = "93000000-0000-7000-8000-000000000304"
	settlementPG18ClaimRace      = "93000000-0000-7000-8000-000000000307"
	settlementPG18ClaimCommA     = "93000000-0000-7000-8000-000000000308"
	settlementPG18ClaimCommB     = "93000000-0000-7000-8000-000000000309"
	settlementPG18ClaimCommFault = "93000000-0000-7000-8000-000000000310"
	settlementPG18ClaimWrongAmt  = "93000000-0000-7000-8000-000000000311"
	settlementPG18ClaimWrongCur  = "93000000-0000-7000-8000-000000000312"
	settlementPG18ClaimCapture   = "93000000-0000-7000-8000-000000000313"
	settlementPG18ClaimFulfil    = "93000000-0000-7000-8000-000000000314"
	settlementPG18ClaimAudit     = "93000000-0000-7000-8000-000000000315"
	settlementPG18ClaimRenewal   = "93000000-0000-7000-8000-000000000319"
	settlementPG18CancelledOrder = "93000000-0000-7000-8000-000000000401"
	settlementPG18ExpiredOrder   = "93000000-0000-7000-8000-000000000402"
	settlementPG18RenewalOrder   = "93000000-0000-7000-8000-000000000403"
	settlementPG18CorruptMissing = "93000000-0000-7000-8000-000000000404"
	settlementPG18CorruptAmount  = "93000000-0000-7000-8000-000000000405"
	settlementPG18CancelledKey   = "93000000-0000-7000-8000-000000000305"
	settlementPG18ExpiredKey     = "93000000-0000-7000-8000-000000000306"
)

func settlementPG18Claim(id, scope, key, hashSeed string, lease time.Time) middleware.IdempotencyClaim {
	return settlementPG18ClaimFor(settlementPG18User, id, scope, key, hashSeed, lease)
}

func settlementPG18ClaimFor(userID, id, scope, key, hashSeed string, lease time.Time) middleware.IdempotencyClaim {
	actorHash := sha256.Sum256([]byte(userID))
	return middleware.IdempotencyClaim{
		ID: id, TenantID: settlementPG18Tenant, ActorID: userID,
		Scope: scope, StorageScope: fmt.Sprintf("%s:actor:%x", scope, actorHash[:12]),
		Key: key, RequestHash: sha256.Sum256([]byte(hashSeed)),
		Generation: 1, LockedUntil: lease,
	}
}

// settlementPG18InsertClaim 预置一条 claim，并返回带真实 id 的那一份。
//
// 不写 id 这一列：产品代码（middleware/idempotency.go）也不写，它靠
// DEFAULT uuidv7() 生成，所以列级授权只给了它实际需要的那几列。测试原先
// 显式插 id，撞在这道最小权限上——正确的方向是让测试跟上权限，而不是为了
// 让测试跑通去放宽生产的授权面。
//
// id 由数据库回填后必须交还给调用方：claim 会被传进 CreateRenewal，服务层
// 要靠它关联这条记录。
func settlementPG18InsertClaim(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	claim middleware.IdempotencyClaim) middleware.IdempotencyClaim {
	t.Helper()
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO idempotency_keys
				(tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
			VALUES ($1,$2,$3,$4,$5::uuid,$6)
			RETURNING id::text`,
			claim.TenantID, claim.StorageScope, claim.Key,
			claim.RequestHash[:], claim.ActorID, claim.LockedUntil).Scan(&claim.ID); err != nil {
			t.Fatalf("insert settlement idempotency claim: %v", err)
		}
	})
	return claim
}

type settlementPG18WebhookResult struct {
	index int
	out   *PaymentWebhookOutput
	err   error
}

func settlementPG18RunConcurrentWebhooks(t *testing.T, ctx context.Context, service *Service,
	inputs []PaymentWebhookInput) []*PaymentWebhookOutput {
	t.Helper()
	start := make(chan struct{})
	results := make(chan settlementPG18WebhookResult, len(inputs))
	var ready sync.WaitGroup
	ready.Add(len(inputs))
	for i := range inputs {
		index := i
		in := inputs[i]
		go func() {
			ready.Done()
			<-start
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			out, err := service.HandlePaymentWebhook(callCtx, settlementPG18Tenant, in)
			results <- settlementPG18WebhookResult{index: index, out: out, err: err}
		}()
	}
	ready.Wait()
	close(start)

	outputs := make([]*PaymentWebhookOutput, len(inputs))
	for range inputs {
		result := <-results
		if result.err != nil {
			if settlementPG18IsDeadlock(result.err) {
				t.Fatalf("concurrent settlement returned SQLSTATE 40P01: %v", result.err)
			}
			t.Fatalf("concurrent settlement failed: %v", result.err)
		}
		if result.out == nil {
			t.Fatal("concurrent settlement returned nil output without error")
		}
		outputs[result.index] = result.out
	}
	return outputs
}

func settlementPG18RunWebhookRace(t *testing.T, ctx context.Context, service *Service, count int,
	input func(int) PaymentWebhookInput) *PaymentWebhookOutput {
	t.Helper()
	inputs := make([]PaymentWebhookInput, count)
	for i := range inputs {
		inputs[i] = input(i)
	}
	outputs := settlementPG18RunConcurrentWebhooks(t, ctx, service, inputs)
	processed, handled := 0, 0
	var winner *PaymentWebhookOutput
	for _, out := range outputs {
		switch {
		case out.Processed && !out.AlreadyHandled:
			processed++
			winner = out
		case out.AlreadyHandled && !out.Processed:
			handled++
		default:
			t.Fatalf("concurrent settlement returned invalid disposition: %#v", out)
		}
	}
	if processed != 1 || handled != count-1 {
		t.Fatalf("concurrent settlement dispositions processed/already=%d/%d want=1/%d",
			processed, handled, count-1)
	}
	return winner
}

func settlementPG18IsDeadlock(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "40P01"
}

func settlementPG18Time(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func settlementPG18InTx(t *testing.T, ctx context.Context, pool *platformdb.Pool, fn func(pgx.Tx)) {
	t.Helper()
	if err := pool.InTx(ctx, platformdb.Scope{TenantID: settlementPG18Tenant, ActorID: settlementPG18User}, func(tx pgx.Tx) error {
		fn(tx)
		return nil
	}); err != nil {
		t.Fatalf("settlement PG18 query transaction: %v", err)
	}
}

func settlementPG18AssertRuntimeACL(t *testing.T, ctx context.Context, pool *platformdb.Pool) {
	t.Helper()
	var sessionUser, currentUser, rowSecurity string
	var canLogin, superuser, inherit, bypass bool
	if err := pool.QueryRow(ctx, `
		SELECT session_user,current_user,current_setting('row_security'),
		       rolcanlogin,rolsuper,rolinherit,rolbypassrls
		  FROM pg_roles WHERE rolname=current_user`).Scan(
		&sessionUser, &currentUser, &rowSecurity, &canLogin, &superuser, &inherit, &bypass,
	); err != nil {
		t.Fatal(err)
	}
	if sessionUser != "aegis_app" || currentUser != "aegis_app" || rowSecurity != "on" ||
		!canLogin || superuser || inherit || bypass {
		t.Fatalf("unsafe runtime role session=%s current=%s row_security=%s login=%v super=%v inherit=%v bypass=%v",
			sessionUser, currentUser, rowSecurity, canLogin, superuser, inherit, bypass)
	}
	var memberships int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_auth_members m
		JOIN pg_roles member_role ON member_role.oid=m.member
		JOIN pg_roles target_role ON target_role.oid=m.roleid
		WHERE member_role.rolname='aegis_app' OR target_role.rolname='aegis_app'`).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 0 {
		t.Fatalf("aegis_app role memberships=%d want=0", memberships)
	}
	if _, err := pool.Exec(ctx, `SET ROLE postgres`); !platformdb.IsInsufficientPrivilege(err) {
		t.Fatalf("SET ROLE postgres error=%v want SQLSTATE 42501", err)
	}

	tables := []string{
		"orders", "order_items", "payment_providers", "payment_intents", "payments",
		"payment_events", "order_reservations", "order_stock_reservations",
		"order_purchase_limit_reservations", "coupon_redemptions", "balance_holds",
		"late_payment_cases", "order_reservation_events", "ledger_accounts",
		"ledger_transactions", "ledger_entries", "subscriptions", "audit_events",
		"commission_entries", "idempotency_keys",
	}
	var protected int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relname=ANY($1::text[])
		  AND c.relrowsecurity AND c.relforcerowsecurity`, tables).Scan(&protected); err != nil {
		t.Fatal(err)
	}
	if protected != len(tables) {
		t.Fatalf("forced-RLS tables=%d want=%d", protected, len(tables))
	}

	// 权限模型已从列级白名单改回表级，这里改验现在真正要保证的东西。
	//
	// 原先这段断言的是「哪几列能写、哪几列不能」——payments.amount 不可改、
	// ledger_accounts.balance_signed 不可改、commission_entries 的身份列不可改。
	// 那套列级最小权限防的是「应用被攻破后的越权写」，设计没错，但维护成本
	// 在当前阶段压过了收益：同一份清单散在迁移与权限脚本两处，每加一列都要
	// 回头改两处，而漏改只在收窄过的库上才显形（开发机是超级用户，永远看不
	// 见）。orders 就这么漏过两列，让全新部署的库根本下不了单。详见
	// deploy/configure-app-role.sql 末尾那段说明。
	//
	// 放弃列级之后，仍然必须成立的是下面这些。它们防的是「已经写进去的钱账
	// 被改写或抹掉」，跟攻击面大小无关，属于账本自身的完整性。
	var writableEvidence []string
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(array_agg(t ORDER BY t), ARRAY[]::text[])
		  FROM unnest(ARRAY['ledger_entries','ledger_transactions',
		                    'gift_card_redemptions','traffic_reset_logs']) AS t
		 WHERE has_table_privilege('aegis_app','public.'||t,'UPDATE')
		    OR has_table_privilege('aegis_app','public.'||t,'DELETE')`).
		Scan(&writableEvidence); err != nil {
		t.Fatal(err)
	}
	if len(writableEvidence) > 0 {
		t.Fatalf("这些表是只追加的证据流水，不该允许改写或删除: %v", writableEvidence)
	}

	// 幂等资源绑定那两个函数只给运行时角色，不能对 PUBLIC 开放——它们能直接
	// 改写幂等证据，是绕过整套重放保护的捷径。
	var binderExec, completerExec, publicBinder, publicCompleter bool
	if err := pool.QueryRow(ctx, `
		SELECT has_function_privilege('aegis_app','app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)','EXECUTE'),
		       has_function_privilege('aegis_app','app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)','EXECUTE'),
		       has_function_privilege('public','app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)','EXECUTE'),
		       has_function_privilege('public','app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)','EXECUTE')`).
		Scan(&binderExec, &completerExec, &publicBinder, &publicCompleter); err != nil {
		t.Fatal(err)
	}
	if !binderExec || !completerExec || publicBinder || publicCompleter {
		t.Fatalf("幂等绑定函数的授权不对 runtime=%v/%v public=%v/%v",
			binderExec, completerExec, publicBinder, publicCompleter)
	}

	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		var shadow int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM payment_providers
			WHERE id='93000000-0000-7000-8000-000000000082'::uuid`).Scan(&shadow); err != nil {
			t.Fatal(err)
		}
		if shadow != 0 {
			t.Fatalf("cross-tenant provider visible through RLS count=%d", shadow)
		}
	})
	if err := pool.InTx(ctx, platformdb.Scope{
		TenantID: "93000000-0000-7000-8000-000000000002",
	}, func(tx pgx.Tx) error {
		var visible int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM commission_entries
			WHERE id='93000000-0000-7000-8000-000000000491'::uuid`).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			return fmt.Errorf("cross-tenant commission visible through RLS count=%d", visible)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// payment_events.raw_payload 的写保护，随列级权限一起放弃了。
	//
	// 这里原本断言运行时角色改不动这一列——渠道原始回调是取证材料，改了就
	// 没法复盘。放弃列级白名单之后这项保护不复存在：payment_events 上只剩
	// 一个 no_delete 触发器，拦删不拦改。
	//
	// 明写在这里而不是默默删掉断言：这是那次取舍的实际代价，将来若要把它
	// 找回来，正确的位置是给 payment_events 加一个 BEFORE UPDATE 触发器，
	// 而不是重新收窄整套列级权限。
	var rawPayloadWritable bool
	if err := pool.QueryRow(ctx,
		`SELECT has_column_privilege('aegis_app','public.payment_events','raw_payload','UPDATE')`).
		Scan(&rawPayloadWritable); err != nil {
		t.Fatal(err)
	}
	if !rawPayloadWritable {
		t.Log("marker=settlement_pg18_raw_payload_still_protected（列级保护似乎回来了，可以收紧这条断言）")
	}

	// 佣金的身份与金额仍然改不动、删不掉——只是拦它的从权限层换成了
	// 00040 的守卫触发器，所以错误码从 42501 变成 check_violation。
	// 保护本身没有减弱，减弱的只是「谁来拦」。
	for name, statement := range map[string]string{
		"commission identity UPDATE": `UPDATE commission_entries SET referrer_user_id=referrer_user_id WHERE id IN (SELECT id FROM commission_entries LIMIT 1)`,
		"commission amount UPDATE":   `UPDATE commission_entries SET commission_amount=commission_amount+1 WHERE id IN (SELECT id FROM commission_entries LIMIT 1)`,
		"commission DELETE":          `DELETE FROM commission_entries WHERE id IN (SELECT id FROM commission_entries LIMIT 1)`,
	} {
		err := pool.InTx(ctx, platformdb.Scope{TenantID: settlementPG18Tenant, ActorID: settlementPG18User}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, statement)
			return err
		})
		if err == nil {
			t.Fatalf("%s 竟然成功了：佣金的身份、金额与存在性都不该被运行时角色改动", name)
		}
		if !platformdb.IsInsufficientPrivilege(err) && !platformdb.IsCheckViolation(err) {
			t.Fatalf("%s 被拒绝的方式不对 error=%v（期望权限或守卫拦截）", name, err)
		}
	}
	// 佣金守卫必须留在 RLS 之下。
	//
	// 它是 BEFORE INSERT 触发器，而 BEFORE 触发器必然先于 RLS 的 WITH CHECK
	// 执行——跨租户插入先撞上的是它，不是 RLS。这本身没问题，前提是它自己也
	// 看不见外租户的数据：SECURITY INVOKER 的守卫查 referrals 时同样受 RLS
	// 约束，于是无论那条关系是否真实存在，都报同一句 missing。
	//
	// 一旦有人给它加上 SECURITY DEFINER，它就会绕过租户隔离读到外租户的
	// referral，错误信息随即开始区分「关系不存在」和「关系存在但字段不符」——
	// 那是一个可以用来枚举其他租户推荐关系的侧信道。
	var commissionGuardIsDefiner bool
	if err := pool.QueryRow(ctx, `
		SELECT p.prosecdef FROM pg_proc p
		  JOIN pg_namespace n ON n.oid=p.pronamespace
		 WHERE n.nspname='app' AND p.proname='guard_commission_entry'`).Scan(&commissionGuardIsDefiner); err != nil {
		t.Fatalf("查 app.guard_commission_entry 的安全属性: %v", err)
	}
	if commissionGuardIsDefiner {
		t.Fatal("app.guard_commission_entry 是 SECURITY DEFINER：它会绕过 RLS 读到外租户的 referral，错误信息将泄露存在性")
	}

	// 跨租户插入必须被拒。42501 是 RLS 拒的，23514 是守卫拒的，两者都算——
	// 上面那条断言已经保证了后者不泄露信息。
	if err := pool.InTx(ctx, platformdb.Scope{TenantID: settlementPG18Tenant, ActorID: settlementPG18User}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO commission_entries
			(tenant_id,referrer_user_id,referee_user_id,order_id,currency,
			 base_amount,rate_bp,commission_amount,frozen_until,review_required)
			VALUES ('93000000-0000-7000-8000-000000000002'::uuid,$1::uuid,$2::uuid,
			        $3::uuid,'CNY',500,1000,50,now()+interval '1 day',false)`,
			settlementPG18Referrer, settlementPG18CommissionUser, settlementPG18ExpiredOrder)
		return err
	}); !platformdb.IsInsufficientPrivilege(err) && !platformdb.IsCheckViolation(err) {
		t.Fatalf("cross-tenant commission INSERT error=%v want SQLSTATE 42501 或 23514", err)
	}
}
