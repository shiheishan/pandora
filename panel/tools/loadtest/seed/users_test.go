package seed

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/crypto"
)

func counterGen(prefix string) func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		return fmt.Sprintf("%s%012d", prefix, n), nil
	}
}

func testTerms() *planTerms {
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	return &planTerms{PlanID: "plan", VersionID: "version", PriceID: "price", Currency: "CNY", Amount: 990,
		PeriodStart: now, PeriodEnd: now.AddDate(0, 1, 0)}
}

func TestBuildUserBatchColumns(t *testing.T) {
	ns := newNamespace("ci", "abc123")
	b, err := buildUserBatch(ns, 10, 3, counterGen("00000000-0000-7000-8000-"), counterGen("tok-aaaaaaaaaaaaaaaa-"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cols := [][]string{b.UserIDs, b.Emails, b.SubIDs, b.Tokens, b.TokenPrefixes}
	for _, c := range cols {
		if len(c) != 3 {
			t.Fatalf("column length %d, want 3", len(c))
		}
	}
	if len(b.TokenHashes) != 3 || len(b.Sealed) != 3 {
		t.Fatal("byte columns must match the batch size")
	}
	if b.Emails[0] != ns.Email(10) || b.Emails[2] != ns.Email(12) {
		t.Fatalf("emails must follow the global user index: %v", b.Emails)
	}
	// 用户与订阅主键来自同一个生成器、交替取号，不会撞
	if b.UserIDs[0] == b.SubIDs[0] {
		t.Fatal("user and subscription ids must differ")
	}
	for k, tok := range b.Tokens {
		if b.TokenPrefixes[k] != tok[:8] || !bytes.Equal(b.TokenHashes[k], crypto.HashToken(tok)) {
			t.Fatalf("token %d prefix/hash do not follow the panel's credential lookup", k)
		}
		if b.Sealed[k] != nil {
			t.Fatal("without a master key nothing is sealed")
		}
	}
}

func TestBuildUserBatchSealsWithSubscriptionAAD(t *testing.T) {
	env, err := crypto.NewEnvelope(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	seal := func(token, subID string) ([]byte, error) { return env.Seal([]byte(token), []byte(subID)) }
	b, err := buildUserBatch(newNamespace("ci", "abc123"), 0, 2, newV7, newSubscriptionToken, seal)
	if err != nil {
		t.Fatal(err)
	}
	for k := range b.Tokens {
		plain, err := env.Open(b.Sealed[k], []byte(b.SubIDs[k]))
		if err != nil || string(plain) != b.Tokens[k] {
			t.Fatalf("sealed token %d does not open with its subscription id: %v", k, err)
		}
		if _, err := env.Open(b.Sealed[k], []byte(b.SubIDs[1-k])); err == nil {
			t.Fatal("ciphertext must be bound to its own subscription")
		}
	}
}

func TestBuildUserBatchRejectsShortTokens(t *testing.T) {
	short := func() (string, error) { return "short", nil }
	if _, err := buildUserBatch(newNamespace("ci", "abc123"), 0, 1, newV7, short, nil); err == nil {
		t.Fatal("tokens shorter than the subscribe endpoint's minimum must be refused")
	}
}

var placeholder = regexp.MustCompile(`\$(\d+)`)

// 每条语句：占位符从 $1 连续编号到 len(args)，数组参数都与批次等长
func TestBatchStatementsBindEveryArgument(t *testing.T) {
	const n = 4
	b, err := buildUserBatch(newNamespace("ci", "abc123"), 0, n, newV7, newSubscriptionToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	terms := testTerms()
	stmts := map[string]stmt{
		"users":         b.usersStmt(testTenant),
		"passwords":     b.passwordsStmt(testTenant, "phc-fixture"),
		"subscriptions": b.subscriptionsStmt(testTenant, terms),
		"activate":      b.activateStmt(testTenant),
		"events":        b.eventsStmt(testTenant, terms),
		"quotas":        b.quotasStmt(testTenant, terms),
		"credentials":   b.credentialsStmt(testTenant, terms),
	}
	for name, s := range stmts {
		maxIdx := 0
		seen := map[int]bool{}
		for _, m := range placeholder.FindAllStringSubmatch(s.SQL, -1) {
			i, _ := strconv.Atoi(m[1])
			seen[i] = true
			maxIdx = max(maxIdx, i)
		}
		if maxIdx != len(s.Args) || len(seen) != len(s.Args) {
			t.Fatalf("%s: placeholders 1..%d (%d distinct) vs %d args", name, maxIdx, len(seen), len(s.Args))
		}
		if s.Args[0] != testTenant {
			t.Fatalf("%s: first argument must be the tenant", name)
		}
		for i, a := range s.Args {
			if v := reflect.ValueOf(a); v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8 && v.Len() != n {
				t.Fatalf("%s: array argument $%d has %d elements, want %d", name, i+1, v.Len(), n)
			}
		}
		if !strings.Contains(s.SQL, "tenant_id") {
			t.Fatalf("%s: every write must carry tenant_id", name)
		}
	}
	// 订阅先插 pending、再经状态机触发器转 active，不直接插 active
	if !strings.Contains(stmts["subscriptions"].SQL, "'pending'") || !strings.Contains(stmts["activate"].SQL, "status = 'pending'") {
		t.Fatal("subscriptions must be created pending and activated through the transition trigger")
	}
	if !strings.Contains(stmts["subscriptions"].SQL, "RETURNING id::text, node_uid") {
		t.Fatal("node_uid must come back from the insert")
	}
}

func TestRetireTouchesOnlyLoadtestMarkers(t *testing.T) {
	for _, want := range []string{"u.email LIKE $2", "'expired'", "subscription_events", "subscription_credentials"} {
		if !strings.Contains(expireSubscriptionsSQL, want) {
			t.Fatalf("expire statement lacks %q", want)
		}
	}
	if strings.Contains(expireSubscriptionsSQL, "DELETE") {
		t.Fatal("retiring must never delete: subscriptions anchor append-only history")
	}
	nodes := make([]nodeVersion, 250)
	got := retireBatches(nodes, retireBatchSize)
	if len(got) != 3 || len(got[0]) != 100 || len(got[2]) != 50 {
		t.Fatalf("retireBatches split = %d batches", len(got))
	}
	if retireBatches(nil, retireBatchSize) != nil {
		t.Fatal("no nodes, no batches")
	}
}
