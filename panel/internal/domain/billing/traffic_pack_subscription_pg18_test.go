package billing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// checkTrafficPackSubscriptionPG18 是 TestTrafficPackOrderPG18 的子测试（traffic_pack 域），证明流量包
// 挂在订阅上（购买模型统一 Q5，迁移 00137 / 00138）：
//
//  1. 从生效中的那份转出：服务 409，直接改库被守卫拒绝；摘回为空同样拒绝；
//  2. 那份停用后转出成功：每笔一条转移流水，纪元推进；重复调用转 0；
//  3. 未分配的转到一份在用的上；转到别人的订阅 404；
//  4. 不写流水直接改 subscription_id：提交时被约束触发器拒绝；
//  5. 00138 回填三档：生效中到期最晚 → 过期 30 天内到期最晚 → 留空；重跑零改动；
//  6. 回填挂上的包（只有 migration 流水）同样不能从生效中的那份转出（00157 删掉了「挪一次」的
//     例外）：服务 409；直接改库，同一事务先写 user 流水、或照 00137 情形 3 先补 migration 流水，
//     都被改挂守卫拒；那份停用后照规则 2 转走。
func checkTrafficPackSubscriptionPG18(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	admin *pgx.Conn, service *Service, fx orderReleasePG18Fixture) {
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("query %s: %v", sql, err)
		}
		return v
	}
	inTx := func(user string, fn func(tx pgx.Tx) error) error {
		return pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: user}, fn)
	}
	newUser := func(label string) string {
		id := uuid.NewString()
		must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$4,'active')`,
			id, fx.tenant, "tps-"+label+"-"+id[:8]+"@example.test", "Pack sub "+label)
		return id
	}
	newSub := func(user string) string {
		t.Helper()
		var sub string
		if err := inTx(user, func(tx pgx.Tx) error {
			var err error
			sub, err = service.grantPlanDirect(ctx, tx, fx.tenant, user, fx.plan, fx.price)
			return err
		}); err != nil {
			t.Fatalf("open a subscription: %v", err)
		}
		return sub
	}
	grant := func(user string, sub *string, bytes int64) string {
		t.Helper()
		var id string
		if err := inTx(user, func(tx pgx.Tx) error {
			var err error
			id, err = GrantTrafficPackTx(ctx, tx, fx.tenant, user, sub, "admin", uuid.NewString(), bytes)
			return err
		}); err != nil {
			t.Fatalf("grant a pack: %v", err)
		}
		return id
	}
	attachedTo := func(grantID string) string {
		var s string
		if err := admin.QueryRow(ctx, `SELECT coalesce(subscription_id::text, '') FROM traffic_pack_grants
			WHERE id=$1::uuid`, grantID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	epoch := func() int64 {
		return scalar(`SELECT last_value + is_called::int FROM node_delivery_epoch`)
	}
	moveDirect := func(user, grantID string, to any, immediate bool) error {
		return inTx(user, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE traffic_pack_grants SET subscription_id = $2::uuid WHERE id = $1::uuid`,
				grantID, to); err != nil {
				return err
			}
			if immediate {
				_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
				return err
			}
			return nil
		})
	}

	u := newUser("owner")
	x, y := newSub(u), newSub(u)
	g1 := grant(u, &x, 1000)

	// 1) 从生效中的那份转出：服务 409，直接改库被 BEFORE 守卫拒绝；摘回为空也拒绝
	_, err := service.TransferTrafficPacks(ctx, fx.tenant, TrafficPackTransferInput{UserID: u, From: &x, To: y})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeConflict || attachedTo(g1) != x {
		t.Fatalf("transfer out of a live subscription err=%v", err)
	}
	if state := orderReleasePG18SQLState(moveDirect(u, g1, y, false)); state != "23514" {
		t.Fatalf("guard on moving out of a live subscription SQLSTATE=%q", state)
	}
	if state := orderReleasePG18SQLState(moveDirect(u, g1, nil, false)); state != "23514" {
		t.Fatalf("guard on detaching SQLSTATE=%q", state)
	}

	// 2) 那份停用后转出成功：每笔一条流水，纪元推进；重复调用转 0
	must(`UPDATE subscriptions SET status='cancelled', cancelled_at=now() WHERE id=$1::uuid`, x)
	before := epoch()
	out, err := service.TransferTrafficPacks(ctx, fx.tenant, TrafficPackTransferInput{UserID: u, From: &x, To: y})
	if err != nil || out.MovedBytes != 1000 || attachedTo(g1) != y || epoch() <= before ||
		scalar(`SELECT count(*) FROM traffic_pack_transfers WHERE grant_id=$1::uuid AND from_subscription_id=$2::uuid
			AND to_subscription_id=$3::uuid AND actor_kind='user' AND remaining_bytes=1000`, g1, x, y) != 1 {
		t.Fatalf("transfer from an ended subscription out=%+v err=%v", out, err)
	}
	if again, err := service.TransferTrafficPacks(ctx, fx.tenant, TrafficPackTransferInput{UserID: u, From: &x, To: y}); err != nil ||
		again.MovedBytes != 0 {
		t.Fatalf("repeated transfer out=%+v err=%v", again, err)
	}
	t.Log("marker=traffic_pack_sub_pg18_transfer_ok")

	// 3) 未分配的转到一份在用的上；转到别人的订阅 404
	g2 := grant(u, nil, 700)
	stranger := newUser("stranger")
	other := newSub(stranger)
	if _, err := service.TransferTrafficPacks(ctx, fx.tenant, TrafficPackTransferInput{UserID: u, To: other}); !errors.As(err, &he) ||
		he.Code != httpx.CodeNotFound {
		t.Fatalf("transfer to a stranger's subscription err=%v", err)
	}
	// 4) 不写流水直接挂上：提交时被约束触发器拒绝
	if state := orderReleasePG18SQLState(moveDirect(u, g2, y, true)); state != "23514" {
		t.Fatalf("move without a transfer record SQLSTATE=%q", state)
	}
	if out, err := service.TransferTrafficPacks(ctx, fx.tenant, TrafficPackTransferInput{UserID: u, To: y}); err != nil ||
		out.MovedBytes != 700 || attachedTo(g2) != y {
		t.Fatalf("transfer of unassigned packs out=%+v err=%v", out, err)
	}
	t.Log("marker=traffic_pack_sub_pg18_guards_ok")

	// 5) 00138 回填：A 两份生效中挂到期最晚的；B 只有过期 30 天内的；C 只有窗口已关的留空；
	// D 生效中优先于过期的
	ua, ub, uc, ud := newUser("bf-a"), newUser("bf-b"), newUser("bf-c"), newUser("bf-d")
	a1, a2 := newSub(ua), newSub(ua)
	must(`UPDATE subscriptions SET current_period_end = now() + interval '90 days' WHERE id=$1::uuid`, a2)
	bs := newSub(ub)
	must(`UPDATE subscriptions SET status='expired', current_period_start = now() - interval '33 days',
		current_period_end = now() - interval '3 days' WHERE id=$1::uuid`, bs)
	cs := newSub(uc)
	must(`UPDATE subscriptions SET status='expired', current_period_start = now() - interval '70 days',
		current_period_end = now() - interval '40 days', renewal_closed_at = now() - interval '10 days'
		WHERE id=$1::uuid`, cs)
	dLive, dExpired := newSub(ud), newSub(ud)
	must(`UPDATE subscriptions SET status='expired', current_period_end = now() + interval '1 day' WHERE id=$1::uuid`, dExpired)
	ga, gb, gc, gd := grant(ua, nil, 100), grant(ub, nil, 200), grant(uc, nil, 300), grant(ud, nil, 400)
	runBackfill := func() {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00138_traffic_pack_grant_backfill.sql"))
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		up, down := strings.Index(src, "-- +goose Up"), strings.Index(src, "-- +goose Down")
		if _, err := admin.Exec(ctx, src[up:down], pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("run 00138 up: %v", err)
		}
	}
	runBackfill()
	if attachedTo(ga) != a2 || attachedTo(gb) != bs || attachedTo(gc) != "" || attachedTo(gd) != dLive {
		t.Fatalf("backfill a=%s(want %s) b=%s(want %s) c=%q d=%s(want %s), a1=%s",
			attachedTo(ga), a2, attachedTo(gb), bs, attachedTo(gc), attachedTo(gd), dLive, a1)
	}
	moves := scalar(`SELECT count(*) FROM traffic_pack_transfers WHERE actor_kind='migration' AND tenant_id=$1`, fx.tenant)
	if moves < 3 || scalar(`SELECT count(*) FROM traffic_pack_transfers WHERE actor_kind='migration'
		AND grant_id = ANY($1::uuid[]) AND from_subscription_id IS NULL`, []string{ga, gb, gd}) != 3 {
		t.Fatalf("backfill transfer records=%d", moves)
	}
	runBackfill()
	if again := scalar(`SELECT count(*) FROM traffic_pack_transfers WHERE actor_kind='migration' AND tenant_id=$1`,
		fx.tenant); again != moves {
		t.Fatalf("rerunning the backfill wrote %d more records", again-moves)
	}
	t.Log("marker=traffic_pack_sub_pg18_backfill_ok")

	// 6) 回填挂上的包（ga 只有 migration 流水，挂在生效中的 a2）同样不能从 a2 转出（用户 2026-10-09
	// 删掉了「升级前旧包挪一次」，00157）：服务 409；直接改库、哪怕同一事务先写了 user 流水，也被改挂
	// 守卫拒（拒绝来自守卫那一条，不是提交时的流水检查）；余额原地不动、没有多出非回填流水。
	// a2 彻底停用后照规则 2 转到 a1。
	if _, err := service.TransferTrafficPacks(ctx, fx.tenant, TrafficPackTransferInput{UserID: ua, From: &a2, To: a1}); !errors.As(err, &he) ||
		he.Code != httpx.CodeConflict {
		t.Fatalf("transfer of a backfilled pack out of a live subscription err=%v", err)
	}
	err = inTx(ua, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO traffic_pack_transfers
			(tenant_id, grant_id, user_id, from_subscription_id, to_subscription_id, remaining_bytes, actor_kind, actor_id)
			VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 100, 'user', $3::uuid)`, fx.tenant, ga, ua, a2, a1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE traffic_pack_grants SET subscription_id = $2::uuid WHERE id = $1::uuid`, ga, a1)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" ||
		!strings.Contains(pgErr.Message, "can only leave an unattached or ended subscription") || attachedTo(ga) != a2 ||
		scalar(`SELECT count(*) FROM traffic_pack_transfers WHERE grant_id=$1::uuid AND actor_kind <> 'migration'`, ga) != 0 {
		t.Fatalf("guard on moving a backfilled pack out of a live subscription err=%v", err)
	}
	// 再用 00157 收紧掉的那条旧放行写法打：同一事务先补一条 migration 流水再改挂。00137 下余额只有
	// migration 流水、情形 3 成立，提交时的流水检查也过，这一步会成功；00157 下必须被守卫拒。
	err = inTx(ua, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO traffic_pack_transfers
			(tenant_id, grant_id, user_id, from_subscription_id, to_subscription_id, remaining_bytes, actor_kind)
			VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 100, 'migration')`, fx.tenant, ga, ua, a2, a1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE traffic_pack_grants SET subscription_id = $2::uuid WHERE id = $1::uuid`, ga, a1)
		return err
	})
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" ||
		!strings.Contains(pgErr.Message, "can only leave an unattached or ended subscription") || attachedTo(ga) != a2 ||
		scalar(`SELECT count(*) FROM traffic_pack_transfers WHERE grant_id=$1::uuid AND from_subscription_id IS NOT NULL`, ga) != 0 {
		t.Fatalf("guard on the pre-00157 legacy move (migration record + move) err=%v", err)
	}
	must(`UPDATE subscriptions SET status='cancelled', cancelled_at=now() WHERE id=$1::uuid`, a2)
	if out, err := service.TransferTrafficPacks(ctx, fx.tenant, TrafficPackTransferInput{UserID: ua, From: &a2, To: a1}); err != nil ||
		out.MovedBytes != 100 || attachedTo(ga) != a1 {
		t.Fatalf("transfer of a backfilled pack after its subscription ended out=%+v err=%v", out, err)
	}
	t.Log("marker=traffic_pack_sub_pg18_live_source_refused_ok")
}
