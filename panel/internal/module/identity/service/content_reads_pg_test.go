package service

import (
	"testing"

	"github.com/google/uuid"
)

// DisplayNames：有昵称用昵称（去空白），昵称空白用邮箱，查不到的 id 不在结果里。
func TestDisplayNamesPG(t *testing.T) {
	e, ctx := newEnv(t)
	var named, blank, plain uuid.UUID
	for _, c := range []struct {
		dst   *uuid.UUID
		email string
		name  any
	}{
		{&named, "named-dn@example.test", "  小明  "},
		{&blank, "blank-dn@example.test", "   "},
		{&plain, "plain-dn@example.test", nil},
	} {
		if err := e.row(ctx, `INSERT INTO users (email, display_name) VALUES (?, ?) RETURNING id`,
			c.email, c.name).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.svc.DisplayNames(ctx, []uuid.UUID{named, blank, plain, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	want := map[uuid.UUID]string{named: "小明", blank: "blank-dn@example.test", plain: "plain-dn@example.test"}
	if len(got) != len(want) {
		t.Fatalf("DisplayNames = %v", got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("DisplayNames[%s] = %q，want %q", id, got[id], w)
		}
	}
}
