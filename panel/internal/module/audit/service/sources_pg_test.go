package service

import (
	"testing"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

// 两人在审计里出现过同一来源 IP 才算同源；同一人、空 id、没有 IP 的事件都不算。
func TestSharesSource(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	write := func(user uuid.UUID, ip string) {
		t.Helper()
		c := reqCtx(ctx, user, authn.AudiencePublic)
		c = reqmeta.With(c, reqmeta.Meta{IP: ip, UserAgent: "test-agent/1.0"})
		if err := svc.Write(c, Event{Action: uniqueAction(t)}); err != nil {
			t.Fatal(err)
		}
	}
	a, b, c, quiet := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	write(a, "198.51.100.10")
	write(a, "198.51.100.11")
	write(b, "198.51.100.11")
	write(c, "198.51.100.12")
	// 没有来源 IP 的事件（如后台任务）不参与比较
	if err := svc.Write(authn.With(ctx, authn.Principal{UserID: quiet, SessionID: "s", Audience: authn.AudiencePublic}),
		Event{Action: uniqueAction(t)}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		x, y uuid.UUID
		want bool
	}{
		{"共用一个 IP", a, b, true},
		{"顺序无关", b, a, true},
		{"IP 不相交", a, c, false},
		{"一方没有带 IP 的事件", a, quiet, false},
		{"同一人", a, a, false},
		{"空 id", uuid.Nil, a, false},
	}
	for _, tc := range cases {
		got, err := svc.SharesSource(ctx, tc.x, tc.y)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: 得到 %v，想要 %v", tc.name, got, tc.want)
		}
	}
}
