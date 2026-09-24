package audit

import (
	"bytes"
	"context"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestAuthContextFromPrincipal(t *testing.T) {
	actor := "0190a000-0000-7000-8000-000000000001"
	other := "0190a000-0000-7000-8000-000000000002"
	withPrincipal := func(p *httpx.Principal) context.Context {
		return httpx.WithPrincipal(context.Background(), p)
	}
	session := &httpx.Principal{Kind: "admin", UserID: actor, SessionID: "s1"}
	reauthed := &httpx.Principal{Kind: "admin", UserID: actor, SessionID: "s1", ReauthedRecently: true}
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		actor *string
		want  string
	}{
		{"no principal", context.Background(), &actor, ""},
		{"no actor", withPrincipal(session), nil, ""},
		{"actor is someone else", withPrincipal(session), &other, ""},
		{"principal without session", withPrincipal(&httpx.Principal{Kind: "admin", UserID: actor}), &actor, ""},
		{"plain session", withPrincipal(session), &actor, "session"},
		{"recent reauth", withPrincipal(reauthed), &actor, "reauth"},
	} {
		if got := authContextFrom(tc.ctx, tc.actor); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// 00080 之前的记录没有 auth_context：空值不得改变哈希，否则历史链整体失效；
// 非空值必须进哈希，否则改写认证强度不会断链。
func TestChainHashAuthContext(t *testing.T) {
	actor := "0190a000-0000-7000-8000-000000000001"
	base := Entry{ActorKind: "admin", ActorID: &actor, Action: "node.update", Outcome: "success"}
	legacy := chainHash([]byte("prev"), "tenant", base, nil, nil)

	h := chainHash([]byte("prev"), "tenant", base, nil, nil)
	if !bytes.Equal(legacy, h) {
		t.Fatal("empty auth_context changed the hash")
	}
	session, reauth := base, base
	session.AuthContext, reauth.AuthContext = "session", "reauth"
	hs := chainHash([]byte("prev"), "tenant", session, nil, nil)
	hr := chainHash([]byte("prev"), "tenant", reauth, nil, nil)
	if bytes.Equal(hs, legacy) || bytes.Equal(hr, legacy) || bytes.Equal(hs, hr) {
		t.Fatal("auth_context is not bound into entry_hash")
	}
}
