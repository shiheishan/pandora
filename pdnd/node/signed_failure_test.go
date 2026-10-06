// [INPUT]: 依赖 panel 的 SignedClient（真实验签与上报）与 Client（用户走兼容通道），依赖 user_resync_test.go 的 userTableCore / fakeUniProxy 夹具，依赖 net/http/httptest 起一个按生效发布契约签名的假面板
// [OUTPUT]: 对外提供 fakeSignedPanel、rejectingCore、preservingRejectCore 夹具与签名通道坏版本不重复应用、只报一次失败、节点已停时逐轮重试的回归测试
// [POS]: pdnd/node 的签名通道失败台账守卫：同一份装不上的发布每轮都会被面板重新签发（issued_at / signature 变、内容不变），节点端必须认出它是同一版本；config_rollback_test.go 守单次回滚本身，user_resync_test.go 守兼容通道的同类重试

package node

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// ---------------------------------------------------------------------------
// 夹具：按端口拒绝入站的两种内核
// ---------------------------------------------------------------------------

// rejectingCore 是整版回滚的兼容内核：没有 ConfigApplier，坏端口的 AddInbound
// 直接失败，node 随后把上一版重装一遍——每一次失败都是两次入站重建。
type rejectingCore struct {
	*userTableCore
	badPorts map[int]bool
	// rejectOnce 按端口登记接下来要额外失败的次数（例如回滚时端口暂被占用）。
	rejectOnce map[int]int
	addInbound int
}

func (c *rejectingCore) AddInbound(cfg *core.InboundConfig) error {
	c.addInbound++
	if c.badPorts[cfg.Port] {
		return errors.New("端口被内核拒绝")
	}
	if c.rejectOnce[cfg.Port] > 0 {
		c.rejectOnce[cfg.Port]--
		return errors.New("端口暂被占用")
	}
	return c.userTableCore.AddInbound(cfg)
}

// preservingRejectCore 是 NativeCore 式的代际内核：坏端口在预检阶段就被拒，
// 旧代际原样保留（PreviousPreserved），node 只补用户、不重装。
type preservingRejectCore struct {
	*userTableCore
	badPorts   map[int]bool
	applyCalls int
}

func (c *preservingRejectCore) ApplyInbound(cfg *core.InboundConfig, _ *core.Routing) error {
	c.applyCalls++
	if c.badPorts[cfg.Port] {
		return &core.ConfigApplyError{Err: errors.New("预检拒绝"), PreviousPreserved: true}
	}
	return c.userTableCore.AddInbound(cfg)
}

// ---------------------------------------------------------------------------
// 夹具：按生效发布契约签名的假面板（签名通道）+ 兼容通道的用户端点
// ---------------------------------------------------------------------------

const (
	testTenantID = "33333333-3333-4333-8333-333333333333"
	testNodeID   = "22222222-2222-4222-8222-222222222222"
	releaseGood  = "11111111-1111-4111-8111-111111111111"
	releaseBad   = "44444444-4444-4444-8444-444444444444"
	releaseNext  = "55555555-5555-4555-8555-555555555555"
)

type signedReport struct {
	ReleaseID  string `json:"release_id"`
	Generation uint64 `json:"generation"`
	Phase      string `json:"phase"`
	Detail     string `json:"detail"`
}

// fakeSignedPanel 每次拉配置都重新签发：issued_at、expires_at、签名每轮都变，
// 内容不变——和真面板（10 分钟投递窗口）一样，节点端不能拿签名认版本。
type fakeSignedPanel struct {
	uni *fakeUniProxy

	mu         sync.Mutex
	priv       ed25519.PrivateKey
	keyID      string
	releaseID  string
	generation uint64
	port       int
	reports    []signedReport
	// failReports 大于零时，接下来这么多次上报回 503（面板暂时不可用）。
	failReports int
}

func (p *fakeSignedPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/nodes/config-signing-key":
		w.WriteHeader(http.StatusNoContent)
	case "/v1/nodes/effective-config":
		_ = json.NewEncoder(w).Encode(p.release())
	case "/v1/nodes/config/report":
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.failReports > 0 {
			p.failReports--
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var in signedReport
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.reports = append(p.reports, in)
		w.WriteHeader(http.StatusNoContent)
	case "/v1/nodes/heartbeat":
		_ = json.NewEncoder(w).Encode(map[string]any{"node_status": "active"})
	default:
		p.uni.ServeHTTP(w, r)
	}
}

func (p *fakeSignedPanel) release() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	payload, _ := json.Marshal(map[string]any{"server_port": p.port, "protocol": "vless"})
	manifest := []byte(`{"sources":["test"]}`)
	contentSum, manifestSum := sha256.Sum256(payload), sha256.Sum256(manifest)
	content := base64.StdEncoding.EncodeToString(contentSum[:])
	manifestHash := base64.StdEncoding.EncodeToString(manifestSum[:])
	issued := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	expires := issued.Add(5 * time.Minute)
	preimage := "aegis-node-effective-config-release-v1\n" +
		"tenant_id=" + testTenantID + "\n" +
		"node_id=" + testNodeID + "\n" +
		"release_id=" + p.releaseID + "\n" +
		"generation=" + strconv.FormatUint(p.generation, 10) + "\n" +
		"content_sha256=" + content + "\n" +
		"source_manifest_sha256=" + manifestHash + "\n" +
		"key_id=" + p.keyID + "\n" +
		"issued_at=" + issued.Format(time.RFC3339Nano) + "\n" +
		"expires_at=" + expires.Format(time.RFC3339Nano) + "\n"
	return map[string]any{
		"config_contract": "aegis-node-effective-config-release-v1",
		"tenant_id":       testTenantID, "node_id": testNodeID,
		"release_id": p.releaseID, "generation": p.generation,
		"content_sha256": content, "hash": content,
		"source_manifest": json.RawMessage(manifest), "source_manifest_sha256": manifestHash,
		"issued_at": issued, "expires_at": expires, "key_id": p.keyID,
		"payload":   json.RawMessage(payload),
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(p.priv, []byte(preimage))),
	}
}

func (p *fakeSignedPanel) publish(releaseID string, generation uint64, port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releaseID, p.generation, p.port = releaseID, generation, port
}

// phases 返回某个发布收到的各阶段上报，按到达顺序。
func (p *fakeSignedPanel) phases(releaseID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.reports {
		if r.ReleaseID == releaseID {
			out = append(out, r.Phase)
		}
	}
	return out
}

func countPhase(phases []string, want string) int {
	n := 0
	for _, p := range phases {
		if p == want {
			n++
		}
	}
	return n
}

// newSignedFixture 起假面板（发布 releaseGood、两个用户）与签名客户端，还没同步过。
func newSignedFixture(t *testing.T, kernel core.Core) (*Node, *fakeSignedPanel) {
	t.Helper()
	configPub, configPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keySum := sha256.Sum256(configPub)
	keyID := base64.RawURLEncoding.EncodeToString(keySum[:8])

	uni := &fakeUniProxy{}
	uni.setUsers(`"users-1"`, "user-a", "user-b")
	fake := &fakeSignedPanel{uni: uni, priv: configPriv, keyID: keyID}
	fake.publish(releaseGood, 1, 18080)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	signed, err := panel.NewSignedClient(&panel.Identity{
		Server: srv.URL, NodeID: testNodeID, Serial: 1,
		PrivateKey:      base64.StdEncoding.EncodeToString(nodePriv),
		ConfigPublicKey: base64.StdEncoding.EncodeToString(configPub),
		ConfigKeyID:     keyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "token"})
	return NewWithSignedClient(client, kernel, testLogger(), signed), fake
}

// ---------------------------------------------------------------------------
// 回归测试
// ---------------------------------------------------------------------------

// 整版回滚的内核上，同一份装不上的发布连拉三轮，只能真正应用一次。
//
// 原先失败不留记录，每一轮都重新验签、重新应用：新入站装一次、失败、
// 旧入站再装一次——每个拉取周期把所有在线连接断两次，直到面板发新版。
// 失败也每轮都报，面板对同一 report_id 不同 detail 的重复上报回 409。
func TestSignedBadReleaseAppliedOnceWithFullRollback(t *testing.T) {
	kernel := &rejectingCore{userTableCore: newUserTableCore(), badPorts: map[int]bool{18099: true}}
	n, fake := newSignedFixture(t, kernel)
	ctx := context.Background()

	n.syncOnce(ctx)
	if !n.started {
		t.Fatal("首个发布没有装上")
	}
	assertKernelUsers(t, kernel.userTableCore, "首个发布之后", "user-a", "user-b")
	before := kernel.addInbound

	fake.publish(releaseBad, 2, 18099)
	for i := 0; i < 3; i++ {
		n.syncOnce(ctx)
	}
	if got := kernel.addInbound - before; got != 2 {
		t.Fatalf("同一坏发布连拉三轮，入站重建 %d 次，期望 2 次（试装一次 + 回滚一次）", got)
	}
	if got := countPhase(fake.phases(releaseBad), "failed"); got != 1 {
		t.Fatalf("同一坏发布上报 failed %d 次，期望 1 次：%v", got, fake.phases(releaseBad))
	}
	if !n.started || kernel.port != 18080 {
		t.Fatalf("旧发布没有继续服务：started=%v port=%d", n.started, kernel.port)
	}
	// 坏发布挂着的这段时间，用户同步不能被它挡住。
	fake.uni.setUsers(`"users-2"`, "user-a", "user-b", "user-c")
	n.syncOnce(ctx)
	assertKernelUsers(t, kernel.userTableCore, "坏发布挂起期间用户变更之后", "user-a", "user-b", "user-c")

	// 面板发了新版：照常应用。
	fake.publish(releaseNext, 3, 18081)
	n.syncOnce(ctx)
	if kernel.port != 18081 || !n.started {
		t.Fatalf("新发布没有应用：started=%v port=%d", n.started, kernel.port)
	}
	if got := countPhase(fake.phases(releaseNext), "switched"); got != 1 {
		t.Fatalf("新发布 switched 上报 %d 次，期望 1 次", got)
	}
	assertKernelUsers(t, kernel.userTableCore, "新发布之后", "user-a", "user-b", "user-c")
}

// NativeCore 式内核（PreviousPreserved）不重建入站，但每轮重新预检、重新报失败
// 同样是浪费：同一坏发布只预检一次。
func TestSignedBadReleaseNotRetriedWhenPreviousPreserved(t *testing.T) {
	kernel := &preservingRejectCore{userTableCore: newUserTableCore(), badPorts: map[int]bool{18099: true}}
	n, fake := newSignedFixture(t, kernel)
	ctx := context.Background()

	n.syncOnce(ctx)
	fake.publish(releaseBad, 2, 18099)
	for i := 0; i < 3; i++ {
		n.syncOnce(ctx)
	}
	if kernel.applyCalls != 2 {
		t.Fatalf("ApplyInbound 调用 %d 次，期望 2 次（首个发布 + 坏发布预检一次）", kernel.applyCalls)
	}
	if got := countPhase(fake.phases(releaseBad), "failed"); got != 1 {
		t.Fatalf("同一坏发布上报 failed %d 次，期望 1 次：%v", got, fake.phases(releaseBad))
	}
	if !n.started || kernel.port != 18080 {
		t.Fatalf("旧代际没有继续服务：started=%v port=%d", n.started, kernel.port)
	}
}

// 节点不在服务（首个发布就没装上）时，同一发布每轮都要重试——没有旧配置
// 可保，重试没有代价，而端口暂时被占这类故障消失后要尽快恢复。失败只报一次。
func TestSignedRetriesSameReleaseWhileNodeStopped(t *testing.T) {
	kernel := &rejectingCore{userTableCore: newUserTableCore(), badPorts: map[int]bool{18080: true}}
	n, fake := newSignedFixture(t, kernel)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		n.syncOnce(ctx)
	}
	if n.started {
		t.Fatal("首个发布被拒，节点却标记为已启动")
	}
	if kernel.addInbound != 3 {
		t.Fatalf("节点停摆时三轮拉取只试装 %d 次，期望每轮一次", kernel.addInbound)
	}
	if got := countPhase(fake.phases(releaseGood), "failed"); got != 1 {
		t.Fatalf("停摆期间同一发布上报 failed %d 次，期望 1 次：%v", got, fake.phases(releaseGood))
	}

	delete(kernel.badPorts, 18080) // 故障消失，发布没变
	n.syncOnce(ctx)
	if !n.started {
		t.Fatal("故障消失后的那一轮没有装上同一发布")
	}
	if got := countPhase(fake.phases(releaseGood), "switched"); got != 1 {
		t.Fatalf("重试成功后 switched 上报 %d 次，期望 1 次", got)
	}
	assertKernelUsers(t, kernel.userTableCore, "停摆后重试成功之后", "user-a", "user-b")
}

// 回滚也失败、节点停摆之后重试同一坏发布：这次回滚成功，旧配置又在服务，
// 节点就回到「旧配置仍在服务」的处境——不再试装，用户全量补回。
//
// 原先回滚成功的分支不恢复 started：旧入站明明装回去了，节点仍当自己停着，
// 用户不同步、每轮还拿坏发布再重建两次入站。
func TestSignedRollbackRecoveryStopsRetrying(t *testing.T) {
	kernel := &rejectingCore{
		userTableCore: newUserTableCore(), badPorts: map[int]bool{18099: true}, rejectOnce: map[int]int{},
	}
	n, fake := newSignedFixture(t, kernel)
	ctx := context.Background()

	n.syncOnce(ctx)
	before := kernel.addInbound
	fake.publish(releaseBad, 2, 18099)
	kernel.rejectOnce[18080] = 1 // 第一次回滚撞上端口占用
	n.syncOnce(ctx)
	if n.started {
		t.Fatal("回滚失败后节点仍标记为已启动")
	}

	for i := 0; i < 3; i++ {
		n.syncOnce(ctx)
	}
	if !n.started || kernel.port != 18080 {
		t.Fatalf("重试时回滚成功，旧发布却没有恢复服务：started=%v port=%d", n.started, kernel.port)
	}
	if got := kernel.addInbound - before; got != 4 {
		t.Fatalf("入站重建 %d 次，期望 4 次（两轮各试装一次 + 回滚一次，之后不再重试）", got)
	}
	if got := countPhase(fake.phases(releaseBad), "failed"); got != 1 {
		t.Fatalf("同一坏发布上报 failed %d 次，期望 1 次：%v", got, fake.phases(releaseBad))
	}
	assertKernelUsers(t, kernel.userTableCore, "回滚恢复之后", "user-a", "user-b")
}

// 失败上报本身没送到（面板 503）：后面的拉取只补报，不重新应用。
func TestSignedFailedReportRetriedWithoutReapply(t *testing.T) {
	kernel := &preservingRejectCore{userTableCore: newUserTableCore(), badPorts: map[int]bool{18099: true}}
	n, fake := newSignedFixture(t, kernel)
	ctx := context.Background()

	n.syncOnce(ctx)
	fake.publish(releaseBad, 2, 18099)
	fake.mu.Lock()
	fake.failReports = 1
	fake.mu.Unlock()
	for i := 0; i < 3; i++ {
		n.syncOnce(ctx)
	}
	if kernel.applyCalls != 2 {
		t.Fatalf("补报失败上报时重新应用了配置：ApplyInbound %d 次，期望 2 次", kernel.applyCalls)
	}
	if got := countPhase(fake.phases(releaseBad), "failed"); got != 1 {
		t.Fatalf("面板最终收到 failed %d 次，期望恰好 1 次：%v", got, fake.phases(releaseBad))
	}
}
