package nodefabric

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// 推送与断线并发：原先 Close 会关掉 Send，select 同时就绪「<-closed」与「Send <- msg」时
// 随机选中后者就 panic，扇出 worker 没有 recover，整个 aegis-node 进程崩溃（审计实测
// 2000 次里 988 次）。这里每轮新建一条连接，推送方与断开方真并发，panic 一次即红。
// 配合 go test -race 同时证明没有数据竞争。
func TestStreamPushRacesDisconnectWithoutPanic(t *testing.T) {
	rounds := 5000
	if testing.Short() {
		rounds = 2000
	}
	var panics atomic.Int64
	guard := func() {
		if recover() != nil {
			panics.Add(1)
		}
	}
	users := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}
	version := UserSetVersion(users)
	for i := 0; i < rounds; i++ {
		h := NewStreamHub()
		c := NewStreamConn("t", "n", 1)
		h.Add(c)
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			defer guard()
			h.PushConfig("t", "n", json.RawMessage(`{}`), "e")
			h.PushConfig("t", "n", json.RawMessage(`{}`), "e") // 第二条撞满 1 格队列，走「断开」分支
		}()
		go func() {
			defer wg.Done()
			defer guard()
			h.PushUsers("t", "n", users, version)
		}()
		go func() {
			defer wg.Done()
			defer guard()
			runtime.Gosched()
			h.Remove(c)
		}()
		wg.Wait()
		// 已关闭的连接上再推：直接返回，不 panic
		func() {
			defer guard()
			h.pushOne(c, []byte(`{}`))
		}()
	}
	if n := panics.Load(); n != 0 {
		t.Fatalf("推送与断线并发 %d 轮，panic %d 次", rounds, n)
	}
}

// 长时间混跑：节点不停地连上、断开（走 Service 的注册 / 注销），推送方不停地推配置与
// 用户名单，写协程按 api 层的写法读队列直到连接关闭。跑完不 panic、不卡死，-race 下无竞争。
func TestStreamHubChurnUnderConcurrentPushes(t *testing.T) {
	svc := NewService(nil, nil)
	hub := NewStreamHub()
	svc.AttachStream(hub)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	const nodes, cycles, pushers = 8, 300, 4
	stop := make(chan struct{})
	var pushWG sync.WaitGroup
	for p := 0; p < pushers; p++ {
		pushWG.Add(1)
		go func(p int) {
			defer pushWG.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				node := fmt.Sprintf("n%d", i%nodes)
				users := []ProxyUser{{ID: int64(i%5 + 1), UUID: "u"}, {ID: 99, UUID: "fixed"}}
				hub.PushUsers("t", node, users, UserSetVersion(users))
				hub.PushConfig("t", node, json.RawMessage(`{"p":1}`), "e")
				if i%7 == 0 {
					hub.BeginUsersPull("t", node)(UserSetVersion(users), i%2 == 0)
				}
			}
		}(p)
	}

	var connWG sync.WaitGroup
	for n := 0; n < nodes; n++ {
		connWG.Add(1)
		go func(n int) {
			defer connWG.Done()
			for i := 0; i < cycles; i++ {
				c := NewStreamConn("t", fmt.Sprintf("n%d", n), 2)
				svc.RegisterStream(c, log)
				users := []ProxyUser{{ID: 1, UUID: "u"}}
				hub.PushInitialUsers(c, users, UserSetVersion(users), "")
				done := make(chan struct{})
				go func() { // 写协程：照 api/node 的写法，以 Closed() 退出
					defer close(done)
					for read := 0; ; read++ {
						select {
						case <-c.Closed():
							return
						case <-c.Send:
							if read > 3 {
								svc.UnregisterStream(c)
							}
						}
					}
				}()
				runtime.Gosched()
				svc.UnregisterStream(c)
				<-done
			}
		}(n)
	}
	connWG.Wait()
	close(stop)
	pushWG.Wait()
	if total := hub.Total(); total != 0 {
		t.Fatalf("全部注销后仍有 %d 条连接", total)
	}
}

// 节点网关重启后 2000 条连接同时回来、各要一份 5000 人的全量：原先每条连接各编码一份
// （审计实测进程峰值 445 MB，超过 aegis-node 256M 的内存上限）。现在同一版本只编码一次，
// 所有连接的队列里放的是同一份字节，堆的增长与连接数无关。
func TestInitialPushHerdSharesOneEncoding(t *testing.T) {
	const conns, userCount = 2000, 5000
	users := make([]ProxyUser, userCount)
	for i := range users {
		users[i] = ProxyUser{ID: int64(i + 1), UUID: fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i), DeviceLimit: 3}
	}
	version := UserSetVersion(users)
	hub := NewStreamHub()

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	all := make([]*StreamConn, conns)
	var wg sync.WaitGroup
	for i := range all {
		c := NewStreamConn("t", fmt.Sprintf("n%d", i), 16)
		hub.Add(c)
		all[i] = c
		wg.Add(1)
		go func() {
			defer wg.Done()
			hub.PushInitialUsers(c, users, version, "")
		}()
	}
	wg.Wait()

	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)
	first := <-all[0].Send
	for _, c := range all[1:] {
		msg := <-c.Send
		if len(msg) != len(first) || &msg[0] != &first[0] {
			t.Fatal("首推没有共用同一份编码")
		}
	}
	grew := int64(after.HeapInuse) - int64(before.HeapInuse)
	t.Logf("%d 条连接各推 %d 人全量：载荷 %.0f KB 只编码一次，HeapInuse 增长 %.1f MB",
		conns, userCount, float64(len(first))/1024, float64(grew)/1e6)
	// 一份载荷约 0.4 MB；连接对象与队列 2000 条合计几 MB。各编码一份时这里是 800 MB 以上。
	if grew > 64<<20 {
		t.Fatalf("首推后堆增长 %.1f MB，超过 64 MB：编码没有共享", float64(grew)/1e6)
	}
}

// 300 个节点、每池 5000 人，一次付款只多出一个用户：原先给每个节点推一份全量
// （pushTenantUsers 传 previous=nil），约 300 × 0.44 MB。现在连接手上的版本在历史里，
// 推增量。这里量出一次变更实际入队的字节数（含 SSE 帧头尾）。
func TestTenantUserChangePushesDeltaBytes(t *testing.T) {
	const nodes, userCount = 300, 5000
	users := make([]ProxyUser, userCount)
	for i := range users {
		users[i] = ProxyUser{ID: int64(i + 1), UUID: fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i), DeviceLimit: 3}
	}
	hub := NewStreamHub()
	conns := make([]*StreamConn, nodes)
	v1 := UserSetVersion(users)
	for i := range conns {
		conns[i] = NewStreamConn("t", fmt.Sprintf("n%d", i), 4)
		hub.Add(conns[i])
		hub.PushInitialUsers(conns[i], users, v1, "")
		<-conns[i].Send
	}
	changed := append(append([]ProxyUser{}, users...), ProxyUser{ID: userCount + 1, UUID: "ffffffff-0000-4000-8000-000000000001", DeviceLimit: 3})
	v2 := UserSetVersion(changed)
	for i := range conns {
		hub.PushUsers("t", conns[i].NodeID, changed, v2)
	}
	var total int
	for _, c := range conns {
		msg := <-c.Send
		total += len("data: ") + len(msg) + len("\n\n")
		var m StreamMessage
		if err := json.Unmarshal(msg, &m); err != nil || m.Event != EventSyncUserDelta {
			t.Fatalf("event = %q err=%v, want a delta", m.Event, err)
		}
	}
	full, _ := hub.users.fullMessage(hub.users.lookup(v2))
	t.Logf("%d 节点 × %d 用户，新增 1 人：本次推送 %d 字节（每节点 %d）；全量每节点 %d 字节，全推合计 %d",
		nodes, userCount, total, total/nodes, len(full)+8, (len(full)+8)*nodes)
	if total > nodes*1024 {
		t.Fatalf("单人变更推了 %d 字节，增量没有生效", total)
	}
}
