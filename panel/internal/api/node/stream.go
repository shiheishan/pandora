package node

// 节点事件流。
//
// 面板把配置变更和用户变动实时推给节点端，省掉轮询那最多一个周期的等待。
// 传输用 SSE：这条链路只需要单向推送，节点的上行本来就走 REST。
//
// 它是一条快车道，不是唯一通路。连不上、断了、消息丢了，节点端都还有
// 轮询兜底——所以这里的每个失败分支都可以直接放弃连接，不需要重试。

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// streamHeartbeat 是没有事件时的保活间隔。
//
// 中间的 nginx 和云厂商 LB 会在空闲若干分钟后悄悄断掉连接，而两端要到
// 下次写才发现。定期发一个注释帧既维持住中间设备的会话表，也让断连能被
// 及时察觉。20 秒是常见 LB 空闲超时（60s）的三分之一，留足余量。
const streamHeartbeat = 20 * time.Second

// streamSendBuffer 是每条连接的待发队列长度。
//
// 够容纳一小阵突发（比如管理员连续改几次配置），又不至于让一个卡住的
// 节点在内存里堆太多。满了就断开——节点会重连，重连走全量，不丢数据。
// 队列里放的是共享的已编码字节（同一版本的名单所有连接共用一份），不额外占内存。
const streamSendBuffer = 16

// streamWriteTimeout 是每次写一帧（含刷出）的期限。
//
// 事件流解除了网关的全局写超时（要挂几小时），但每一次写仍要有期限：节点断网
// 而 TCP 没断时，写会一直卡在内核发送缓冲上，这条幽灵连接要等 TCP 重传超时
// （Linux 约 15 分钟）才回收，期间推送一直往它的队列里塞。15 秒足够把几百 KB 的
// 全量名单写给一个正常的节点。
const streamWriteTimeout = 15 * time.Second

// streamReconnectHintMax 是建议节点断线后等待的上限（SSE 的 retry 字段，毫秒）。
//
// 每条连接给一个 0–10 秒的随机值：网关重启时几百个节点会在一两秒内一齐重连，
// 各自按建议错开，鉴权与首推就不会挤在同一刻。现有节点端忽略这一行，照旧按自己的
// 退避重连；读到它不影响解析（不是 data 行）。
const streamReconnectHintMax = 10 * time.Second

func (h *handlers) uniStream(w http.ResponseWriter, r *http.Request) {
	n, ok := h.authNode(w, r)
	if !ok {
		return
	}
	if h.d.NodeStream == nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errStreamDisabled))
		return
	}
	rc := http.NewResponseController(w)

	// 解除这条连接的读超时与全局写超时。
	//
	// http.Server 上的 WriteTimeout 是给普通接口兜底的，事件流要挂几小时，
	// 被它管着会稳定地在超时点断开。用 ResponseController 只改当前连接，
	// 不动全局配置；写期限改由每次写帧时各自设置（writeStreamFrame）。
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	// 关掉 nginx 的响应缓冲。不关的话事件会攒在 nginx 里，等攒够一个缓冲
	// 块才发出去——推送变成了另一种形式的批量延迟，而且延迟还不可预测。
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	hint := rand.Int64N(int64(streamReconnectHintMax/time.Millisecond) + 1)
	if err := writeStreamFrame(w, rc, nil, []byte("retry: "+strconv.FormatInt(hint, 10)+"\n\n")); err != nil {
		return
	}

	tenantID := httpx.TenantIDFrom(r.Context())
	conn := nodefabric.NewStreamConn(tenantID, n.ID, streamSendBuffer)
	h.d.Node.RegisterStream(conn, h.d.Log)
	defer h.d.Node.UnregisterStream(conn)

	h.d.Log.Info("节点事件流已连接", "node", n.ID, "在线连接", h.d.NodeStream.Total())
	openedAt := time.Now()
	defer func() {
		// 断开时把这条连接活了多久一起记下来。
		//
		// 这不是凑数的字段：这个端点上线后每条连接都恰好活 20 秒就断，而
		// 日志里只有成对的「已连接 / 已断开」，看上去与正常重连无异，没人
		// 发现推送其实一直没工作。真凶是请求超时中间件，而它切得越规律，
		// 越像是客户端自己在重连。
		//
		// 活不过两个心跳的连接不正常——正常客户端要么挂很久，要么在断网
		// 时才走，不会规律地在这个尺度上来回。
		lived := time.Since(openedAt)
		if lived < 2*streamHeartbeat {
			h.d.Log.Warn("节点事件流很快就断了", "node", n.ID,
				"存活", lived.Round(time.Millisecond).String(),
				"心跳间隔", streamHeartbeat.String())
			return
		}
		h.d.Log.Info("节点事件流已断开", "node", n.ID,
			"存活", lived.Round(time.Second).String())
	}()

	// 连上先推一次用户名单，让节点端有个已知的起点。
	//
	// 不推的话节点端要等到下一次变更才知道自己那份对不对，而在此之前
	// 面板会以为它已经同步了——增量就会基于一个错误的前提去算。
	// 节点报上来的版本与当前版一致时不推（PushInitialUsers）；同一版本的全量
	// 所有连接共用一份编码，几百条连接同时回来也只编码一次。
	if users, version, err := h.d.Node.NodeUserSet(r.Context(), tenantID, n); err == nil {
		h.d.NodeStream.PushInitialUsers(conn, users, version,
			r.Header.Get(nodefabric.StreamUsersVersionHeader))
	}

	ticker := time.NewTicker(streamHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-conn.Closed():
			// hub 把这条连接断了——多半是它消费不过来被丢弃了。
			return
		case msg := <-conn.Send:
			if err := writeStreamFrame(w, rc, streamDataPrefix, msg); err != nil {
				return
			}
		case <-ticker.C:
			// SSE 的注释帧：以冒号开头，客户端会忽略内容，但它足以让
			// 中间设备看到这条连接还活着。
			if err := writeStreamFrame(w, rc, nil, streamKeepalive); err != nil {
				return
			}
		}
	}
}

var errStreamDisabled = errors.New("节点事件流未启用")

var (
	streamDataPrefix = []byte("data: ")
	streamKeepalive  = []byte(": keepalive\n\n")
	streamFrameEnd   = []byte("\n\n")
)

// writeStreamFrame 写一帧并刷出，整帧受 streamWriteTimeout 约束。
//
// prefix 非空时按 SSE 数据帧写：prefix + body + 空行；否则 body 原样写出。
// body 可能是多条连接共享的已编码名单，这里只读、不拷贝（不经 fmt 拼接）。
func writeStreamFrame(w http.ResponseWriter, rc *http.ResponseController, prefix, body []byte) error {
	_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if prefix != nil {
		if _, err := w.Write(prefix); err != nil {
			return err
		}
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if prefix != nil {
		if _, err := w.Write(streamFrameEnd); err != nil {
			return err
		}
	}
	return rc.Flush()
}
