package service

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

func TestActivityByDayPG(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	suf := randomSuffix(t)
	reg := "t.reg." + suf
	login := "t.login." + suf
	order := "t.order." + suf

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	apr1_10 := time.Date(2026, 4, 1, 10, 0, 0, 0, loc)
	apr2_0030 := time.Date(2026, 4, 2, 0, 30, 0, 0, loc)
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, 4, 3, 0, 0, 0, 0, loc)

	hash01 := make([]byte, 32)
	hash01[0] = 0x01
	hash02 := make([]byte, 32)
	hash02[0] = 0x02

	exec := d.Conn(ctx).Exec
	ins := `INSERT INTO audit_events (occurred_at, actor_kind, action, outcome, ip_hash) VALUES (?, 'anonymous', ?, ?, ?)`
	insNoIP := `INSERT INTO audit_events (occurred_at, actor_kind, action, outcome) VALUES (?, 'system', ?, 'success')`

	for _, h := range [][]byte{hash01, hash02} {
		if err := exec(ins, apr1_10, reg, "success", h).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := exec(ins, apr1_10, login, "success", hash01).Error; err != nil {
		t.Fatal(err)
	}
	if err := exec(ins, apr1_10, login, "failure", hash01).Error; err != nil {
		t.Fatal(err)
	}
	if err := exec(insNoIP, apr2_0030, order).Error; err != nil {
		t.Fatal(err)
	}

	got, err := svc.ActivityByDay(ctx, from, to, ActivityActions{
		Registered:   reg,
		Login:        login,
		OrderCreated: order,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len %d want 2: %+v", len(got), got)
	}
	if got[0].Day != "2026-04-01" {
		t.Fatalf("day[0] %q", got[0].Day)
	}
	if got[0].Registered != 2 || got[0].Logins != 1 || got[0].Orders != 0 {
		t.Fatalf("2026-04-01: reg=%d logins=%d orders=%d", got[0].Registered, got[0].Logins, got[0].Orders)
	}
	if got[0].UniqueIPs < 2 {
		t.Fatalf("2026-04-01 unique_ips %d want >= 2", got[0].UniqueIPs)
	}
	if got[1].Day != "2026-04-02" {
		t.Fatalf("day[1] %q", got[1].Day)
	}
	if got[1].Orders != 1 {
		t.Fatalf("2026-04-02 orders %d want 1", got[1].Orders)
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
