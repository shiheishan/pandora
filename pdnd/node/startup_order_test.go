package node

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/panel"
)

// coldStartRace 让两个节点（各自一个兼容面板，配置撞同一个 TCP 端口）在同一个真
// NativeCore 上并发冷启动，返回装上的是哪一个（0 / 1）。节点 0 的面板故意慢一拍：
// 不排队的话谁先拿到配置谁赢，节点 1 几乎总是先到。
func coldStartRace(t *testing.T, port int, ordered bool) int {
	t.Helper()
	kernel := newNativeKernel(t)
	var order *StartupOrder
	if ordered {
		order = NewStartupOrder(2)
	}
	nodes := make([]*Node, 2)
	for i := range nodes {
		fake := &socksPanel{port: port}
		delay := time.Duration(0)
		if i == 0 {
			delay = 30 * time.Millisecond
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(delay)
			fake.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: strconv.Itoa(i + 1), NodeType: "socks", Token: "t"})
		nodes[i] = New(client, kernel, testLogger())
		if order != nil {
			nodes[i].SetStartupOrder(order, i)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.lifeCtx = ctx
			n.startup(ctx)
		}()
	}
	wg.Wait()
	winner := -1
	for i, n := range nodes {
		if n.started {
			if winner != -1 {
				t.Fatal("两个节点都装上了同一个端口")
			}
			winner = i
		}
	}
	if winner == -1 {
		t.Fatal("两个节点都没装上")
	}
	loser := nodes[1-winner]
	var reason interface{ RuntimeReason() string }
	if !errors.As(loser.lastApplyErr, &reason) {
		t.Fatalf("输家的失败原因不是端口占用：%v", loser.lastApplyErr)
	}
	_ = kernel.Close()
	return winner
}

// 同一份冲突配置冷启动 20 次，赢家始终是 nodes[] 里排在前面的那个；不排队时
// 赢家由谁先拿到配置决定（对照组）。
func TestColdStartPortWinnerFollowsNodesOrder(t *testing.T) {
	port := freeTCPPort(t)
	for i := 0; i < 20; i++ {
		if winner := coldStartRace(t, port, true); winner != 0 {
			t.Fatalf("第 %d 次冷启动赢家是节点 %d，期望始终是 nodes[0]", i+1, winner)
		}
	}
	unorderedWins := map[int]int{}
	for i := 0; i < 5; i++ {
		unorderedWins[coldStartRace(t, port, false)]++
	}
	if unorderedWins[1] == 0 {
		t.Fatalf("对照组（不排队）赢家分布 %v：没测出竞速，测试本身失效了", unorderedWins)
	}
}
