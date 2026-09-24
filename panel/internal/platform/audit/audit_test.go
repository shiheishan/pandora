package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

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
	legacy := chainHashV1([]byte("prev"), "tenant", base, nil, nil)

	h := chainHashV1([]byte("prev"), "tenant", base, nil, nil)
	if !bytes.Equal(legacy, h) {
		t.Fatal("empty auth_context changed the hash")
	}
	session, reauth := base, base
	session.AuthContext, reauth.AuthContext = "session", "reauth"
	hs := chainHashV1([]byte("prev"), "tenant", session, nil, nil)
	hr := chainHashV1([]byte("prev"), "tenant", reauth, nil, nil)
	if bytes.Equal(hs, legacy) || bytes.Equal(hr, legacy) || bytes.Equal(hs, hr) {
		t.Fatal("auth_context is not bound into entry_hash")
	}
}

// jsonb 读回的文本（键按长度再按字节排、冒号逗号后带空格、数字按 numeric 写）
// 与写入时 json.Marshal 的字节不同，规范化之后必须相同——这正是第一版的缺陷。
func TestCanonicalJSONIgnoresStorageForm(t *testing.T) {
	type digest struct {
		Zeta  string         `json:"zeta"`
		Alpha int64          `json:"alpha"`
		Rate  float64        `json:"rate"`
		Tiny  float64        `json:"tiny"`
		Big   float64        `json:"big"`
		HTML  string         `json:"html"`
		List  []any          `json:"list"`
		Inner map[string]any `json:"inner"`
	}
	written, err := json.Marshal(digest{Zeta: "z", Alpha: 9007199254740993, Rate: 1.5, Tiny: 1e-7,
		Big: 1e21, HTML: "<a&b>", List: []any{true, nil, "中文"}, Inner: map[string]any{"b": 1, "a": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL 18 对上面那段 JSON 的 jsonb 输出
	stored := []byte(`{"big": 1000000000000000000000, "html": "<a&b>", "list": [true, null, "中文"], "rate": 1.5, "tiny": 0.0000001, "zeta": "z", "alpha": 9007199254740993, "inner": {"a": "x", "b": 1}}`)
	a, err := canonicalJSON(written)
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSON(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical forms differ:\n%s\n%s", a, b)
	}
	// 值不同必须得到不同的规范形式
	for _, other := range []string{
		`{"big": 1000000000000000000000, "html": "<a&b>", "list": [true, null, "中文"], "rate": 1.50001, "tiny": 0.0000001, "zeta": "z", "alpha": 9007199254740993, "inner": {"a": "x", "b": 1}}`,
		`{"big": 1000000000000000000000, "html": "<a&b>", "list": [null, true, "中文"], "rate": 1.5, "tiny": 0.0000001, "zeta": "z", "alpha": 9007199254740993, "inner": {"a": "x", "b": 1}}`,
		`{"big": 1000000000000000000000, "html": "<a&b>", "list": [true, null, "中文"], "rate": 1.5, "tiny": 0.0000001, "zeta": "z", "alpha": 9007199254740992, "inner": {"a": "x", "b": 1}}`,
	} {
		c, err := canonicalJSON([]byte(other))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(a, c) {
			t.Fatalf("different value, same canonical form: %s", other)
		}
	}
	if _, err := canonicalJSON([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

// 第二版口径：每个字段都进哈希，auth_context 空与非空也要区分；
// 可空字段的「不存在」与「空」不同，相邻字段不能互相挪字节。
func TestChainHashV2BindsEveryField(t *testing.T) {
	actor, resource, approval := "a", "r", "p"
	base := chainRecord{Seq: 7, OccurredAt: time.UnixMicro(1_700_000_000_123_456), TenantID: "t",
		ActorKind: "admin", ActorID: &actor, ActorLabel: "label", Action: "node.update",
		ResourceType: "node", ResourceID: &resource, Before: []byte(`{"a":1}`), After: []byte(`{"a":2}`),
		RequestID: "req", APIDomain: "admin", SourceIPHash: []byte{1}, UserAgent: "ua",
		ApprovalID: &approval, Outcome: "success", ErrorCode: "e", SourceIPEnc: []byte{2}, AuthContext: "session"}
	want, err := chainHashV2([]byte("prev"), base)
	if err != nil {
		t.Fatal(err)
	}
	same := base
	same.Before = []byte(`{ "a" : 1.0 }`)
	if got, _ := chainHashV2([]byte("prev"), same); !bytes.Equal(got, want) {
		t.Fatal("storage form of a digest changed the hash")
	}
	empty := ""
	mutations := map[string]func(*chainRecord){
		"seq":           func(r *chainRecord) { r.Seq++ },
		"occurred_at":   func(r *chainRecord) { r.OccurredAt = r.OccurredAt.Add(time.Microsecond) },
		"tenant":        func(r *chainRecord) { r.TenantID = "u" },
		"actor_kind":    func(r *chainRecord) { r.ActorKind = "user" },
		"actor_id nil":  func(r *chainRecord) { r.ActorID = nil },
		"actor_id ''":   func(r *chainRecord) { r.ActorID = &empty },
		"actor_label":   func(r *chainRecord) { r.ActorLabel = "other" },
		"action":        func(r *chainRecord) { r.Action = "node.delete" },
		"shift bytes":   func(r *chainRecord) { r.Action, r.ResourceType = "node.updaten", "ode" },
		"resource_id":   func(r *chainRecord) { r.ResourceID = nil },
		"before":        func(r *chainRecord) { r.Before = []byte(`{"a":3}`) },
		"before null":   func(r *chainRecord) { r.Before = []byte(`null`) },
		"before absent": func(r *chainRecord) { r.Before = nil },
		"after":         func(r *chainRecord) { r.After = []byte(`{"a":2,"b":0}`) },
		"request_id":    func(r *chainRecord) { r.RequestID = "req2" },
		"api_domain":    func(r *chainRecord) { r.APIDomain = "public" },
		"ip_hash":       func(r *chainRecord) { r.SourceIPHash = []byte{9} },
		"user_agent":    func(r *chainRecord) { r.UserAgent = "ub" },
		"approval":      func(r *chainRecord) { r.ApprovalID = nil },
		"outcome":       func(r *chainRecord) { r.Outcome = "denied" },
		"error_code":    func(r *chainRecord) { r.ErrorCode = "" },
		"ip_enc":        func(r *chainRecord) { r.SourceIPEnc = nil },
		"auth reauth":   func(r *chainRecord) { r.AuthContext = "reauth" },
		"auth empty":    func(r *chainRecord) { r.AuthContext = "" },
	}
	for name, mutate := range mutations {
		r := base
		mutate(&r)
		got, err := chainHashV2([]byte("prev"), r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.Equal(got, want) {
			t.Errorf("%s is not bound into entry_hash", name)
		}
	}
	if got, _ := chainHashV2([]byte("prev2"), base); bytes.Equal(got, want) {
		t.Error("prev_hash is not bound into entry_hash")
	}
}

// 第一版存量行：不带摘要的必须严格复算；map 摘要按「键排序紧凑形」能找回；
// 结构体摘要（字段顺序已丢）只能核对链接。
func TestVerifyLegacyRules(t *testing.T) {
	actor := "0190a000-0000-7000-8000-000000000001"
	e := Entry{ActorKind: "admin", ActorID: &actor, Action: "x", Outcome: "success"}
	rec := chainRecord{ActorKind: "admin", ActorID: &actor, Action: "x", Outcome: "success"}

	plain := chainHashV1([]byte("p"), "t", e, nil, nil)
	if verifyLegacy([]byte("p"), "t", rec, plain) != legacyVerified {
		t.Fatal("digest-free legacy row must verify strictly")
	}
	if verifyLegacy([]byte("p"), "t", rec, []byte("forged")) != legacyMismatch {
		t.Fatal("digest-free legacy row with a wrong hash must break")
	}

	mapDigest, _ := json.Marshal(map[string]any{"status": "active", "id": 3, "note": "<b>"})
	withMap := chainHashV1([]byte("p"), "t", e, nil, mapDigest)
	r := rec
	r.After = []byte(`{"id": 3, "note": "<b>", "status": "active"}`)
	if verifyLegacy([]byte("p"), "t", r, withMap) != legacyVerified {
		t.Fatal("map digest must be recovered through the sorted compact form")
	}

	structDigest, _ := json.Marshal(struct {
		Status string `json:"status"`
		ID     int    `json:"id"`
	}{"active", 3})
	withStruct := chainHashV1([]byte("p"), "t", e, nil, structDigest)
	r.After = []byte(`{"id": 3, "status": "active"}`)
	if verifyLegacy([]byte("p"), "t", r, withStruct) != legacyLinkOnly {
		t.Fatal("struct digest with lost field order must fall back to link-only")
	}
}
