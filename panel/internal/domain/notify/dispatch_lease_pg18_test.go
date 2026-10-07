package notify

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// blockingSender 在 Send 里停住，直到放行；记下发送次数。
type blockingSender struct {
	entered chan struct{}
	release chan struct{}
	sends   atomic.Int32
}

func (b *blockingSender) Channel() Channel { return ChannelEmail }

func (b *blockingSender) Send(ctx context.Context, _, _, _ string) error {
	b.sends.Add(1)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// 两个实例同时派发：A 认领后在发信途中（事务早已提交），B 不能再认领同一条。
// 原先认领的锁随选取事务提交就释放了、状态仍是 queued，B 会把它再发一遍。
func TestDispatchClaimLeasePG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "NOTIFY", DatabasePrefix: "pandora_notify_",
		MarkerTable: "pandora_notify_test_marker", CommentTag: "pandora-notify-pg18",
	})
	const (
		tenant = "c19b0000-0000-4000-8000-000000000001"
		user   = "c19b0000-0000-4000-8000-000000000011"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'notify-lease-pg18','Notify Lease','CNY')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,'u@notify-lease.invalid','U','active')`, user, tenant)
	must(`INSERT INTO notification_templates(tenant_id,code,channel,subject,body,category,status) VALUES
		($1,'lease.test','email','s','b','transactional','active')`, tenant)

	sender := &blockingSender{entered: make(chan struct{}, 1), release: make(chan struct{})}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	instanceA := New(app, log, []byte("notify-lease-salt"), sender)
	instanceB := New(app, log, []byte("notify-lease-salt"), sender)
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		_, err := instanceA.Enqueue(ctx, tx, tenant, user, "lease.test", map[string]string{}, "lease-test")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		n   int
		err error
	}
	doneA := make(chan result, 1)
	go func() {
		n, err := instanceA.Dispatch(ctx, tenant, 10)
		doneA <- result{n, err}
	}()
	select {
	case <-sender.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("instance A never started sending")
	}

	// A 正在发：B 认领不到
	if n, err := instanceB.Dispatch(ctx, tenant, 10); err != nil || n != 0 {
		close(sender.release)
		t.Fatalf("instance B claimed a delivery leased to A: n=%d err=%v", n, err)
	}
	var leased bool
	if err := admin.QueryRow(ctx, `SELECT status = 'queued' AND next_retry_at > now() + interval '9 minutes'
		FROM notification_deliveries WHERE tenant_id=$1 AND template_code='lease.test'`, tenant).Scan(&leased); err != nil || !leased {
		close(sender.release)
		t.Fatalf("claimed delivery must stay queued under a lease: leased=%v err=%v", leased, err)
	}

	close(sender.release)
	r := <-doneA
	if r.err != nil || r.n != 1 {
		t.Fatalf("instance A dispatch n=%d err=%v", r.n, r.err)
	}
	var status string
	if err := admin.QueryRow(ctx, `SELECT status FROM notification_deliveries
		WHERE tenant_id=$1 AND template_code='lease.test'`, tenant).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("delivery status=%q err=%v, want sent", status, err)
	}
	if got := sender.sends.Load(); got != 1 {
		t.Fatalf("delivery sent %d times, want exactly once", got)
	}

	// 租约过期（认领的实例中途挂了）：任一实例都能重新认领
	must(`INSERT INTO notification_deliveries(tenant_id,user_id,template_code,channel,dedupe_key,recipient_hash,payload,status,max_attempts,next_retry_at)
		SELECT tenant_id,user_id,template_code,channel,dedupe_key || ':expired-lease',recipient_hash,payload,'queued',max_attempts, now() - interval '1 second'
		  FROM notification_deliveries WHERE tenant_id=$1 AND template_code='lease.test'`, tenant)
	sender.release = make(chan struct{})
	close(sender.release)
	if n, err := instanceB.Dispatch(ctx, tenant, 10); err != nil || n != 1 {
		t.Fatalf("expired lease must be reclaimable: n=%d err=%v", n, err)
	}
}
