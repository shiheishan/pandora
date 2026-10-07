package subscription

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 未认证失败的采样：同一来源每窗口一行，整个进程每窗口有总额度，窗口切换时报出没落库的次数。
func TestFailureSamplerBoundsWrites(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	f := newFailureSampler(time.Minute, 3)
	f.now = clock.Now

	admitted := 0
	for i := range 100 {
		ok, rolled := f.admit("scanner") // 同一来源狂刷
		if rolled != 0 {
			t.Fatalf("request %d reported a rollover inside the window", i)
		}
		if ok {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("one source admitted %d times in a window, want 1", admitted)
	}
	// 换来源：还剩两个额度，之后全部丢弃
	for i, want := range []bool{true, true, false, false} {
		if ok, _ := f.admit("src-" + strconv.Itoa(i)); ok != want {
			t.Fatalf("source %d admitted=%v, want %v", i, ok, want)
		}
	}
	// 窗口切换：报出上一窗口丢了多少（99 + 2），额度与来源记录清零
	clock.Add(time.Minute)
	ok, rolled := f.admit("scanner")
	if !ok || rolled != 101 {
		t.Fatalf("after rollover admitted=%v rolled=%d, want true/101", ok, rolled)
	}
	if _, rolled := f.admit("scanner"); rolled != 0 {
		t.Fatal("rollover must be reported once")
	}
	// 时钟回拨也当作新窗口，不会卡死在旧窗口里
	clock.Add(-time.Hour)
	if ok, _ := f.admit("scanner"); !ok {
		t.Fatal("clock moving backwards must start a new window")
	}
	// nil 采样器（测试里直接构造的 Service）一律落库
	var none *failureSampler
	if ok, rolled := none.admit("x"); !ok || rolled != 0 {
		t.Fatal("nil sampler must admit everything")
	}
}

func TestCheckCredential(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	hash := []byte("0123456789abcdef0123456789abcdef")
	other := []byte("fedcba9876543210fedcba9876543210")
	for _, tc := range []struct {
		name          string
		hash          []byte
		cred, sub     string
		expires, grac *time.Time
		ok            bool
	}{
		{"active", hash, "active", "active", nil, nil, true},
		{"trialing subscription", hash, "active", "trialing", nil, nil, true},
		{"grace subscription", hash, "grace", "grace", nil, nil, true},
		{"hash mismatch", other, "active", "active", nil, nil, false},
		{"revoked credential", hash, "revoked", "active", nil, nil, false},
		{"expired credential", hash, "expired", "active", nil, nil, false},
		{"past deadline", hash, "active", "active", &past, nil, false},
		{"grace extends deadline", hash, "active", "active", &past, &future, true},
		{"grace also past", hash, "grace", "active", &past, &past, false},
		{"past_due subscription", hash, "active", "past_due", nil, nil, false},
		{"cancelled subscription", hash, "active", "cancelled", &future, nil, false},
	} {
		err := checkCredential(tc.hash, hash, tc.cred, tc.expires, tc.grac, tc.sub, now)
		if tc.ok && err != nil {
			t.Errorf("%s: err=%v, want ok", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err=%v, want ErrNotFound", tc.name, err)
		}
	}
}

// 订阅拉取的结构约束：令牌按有唯一索引的哈希查；节点仍只经共用资格查询（经缓存）；
// 认证从不进缓存。
func TestPullUsesHashLookupAndSharedEligibility(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	if !strings.Contains(pullAuthSQL, "sc.token_hash = $2") || strings.Contains(pullAuthSQL, "token_prefix") {
		t.Fatal("pull must look credentials up by the uniquely indexed token hash")
	}
	load := pkg.Decl("Service.LoadPull")
	for _, want := range []string{"pullAuthSQL", "crypto.HashToken(token)", "checkCredential(", "s.cachedNodes("} {
		if !strings.Contains(load, want) {
			t.Fatalf("LoadPull missing %q", want)
		}
	}
	if strings.Contains(load, "s.nodes.") || strings.Contains(load, "listEligibleNodesTx(") {
		t.Fatal("LoadPull must reach nodes only through cachedNodes")
	}
	if cached := pkg.Decl("Service.cachedNodes"); !strings.Contains(cached, "s.ListNodes(ctx, tenantID, c)") {
		t.Fatal("node cache must fill through ListNodes, the shared eligibility query")
	}
	// 写路径：限流计数与日志在拿到凭据行锁之后，同一事务
	rec := pkg.Decl("Service.RecordSuccessfulFetch")
	lock := strings.Index(rec, "UPDATE subscription_credentials")
	insert := strings.Index(rec, "INSERT INTO subscription_fetch_log")
	if lock < 0 || insert < lock || strings.Count(rec, "s.pool.InTx(") != 1 ||
		!strings.Contains(rec, "return ErrRateLimited") {
		t.Fatal("rate limit, fetch log and credential touch must share one transaction, lock first")
	}
}
