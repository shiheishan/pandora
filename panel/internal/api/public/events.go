package public

// SSE 端点。
//
// 一条长连接把该用户有权知道的所有变化推给浏览器：套餐上下架、
// 订单状态、工单回复。前端收到通知后重新拉对应接口，页面自然就更新了，
// 用户不需要按刷新。

import (
	"fmt"
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// sseHeartbeat 是没有事件时的保活间隔。
//
// 中间的 nginx 和云厂商 LB 会在空闲若干分钟后悄悄断掉连接，两端要到下次写
// 才发现。定期发一个注释帧既维持住中间设备的会话表，也让断连能被及时察觉。
// 它同时是「连接活得够不够久」的判据，所以必须是具名常量而不是散在两处的
// 字面量——那两个值一旦走偏，警告要么永不触发，要么天天误报。
const sseHeartbeat = 25 * time.Second

// events 建立一条 SSE 连接。
func (h *handlers) events(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// 没有 Flusher 就没法流式输出，整条连接会被缓冲到结束才发出去 ——
		// 那就完全不是实时了。这种情况只可能是中间套了不支持流式的包装层。
		httpx.Fail(w, r, h.d.Log, httpx.Internal(fmt.Errorf("响应不支持流式输出")))
		return
	}

	// 订阅范围就是鉴权边界：只登记这个用户有权知道的频道。
	// 频道名里带着租户与用户 ID，别人的事件不可能进到这条连接里。
	channels := []string{
		realtime.ChannelPublic(p.TenantID),
		realtime.ChannelUser(p.TenantID, p.UserID),
	}
	events, unsubscribe := h.d.Realtime.Subscribe(channels)
	defer unsubscribe()

	// 记下这条连接活了多久。
	//
	// 这两个端点此前一条连接日志都没有——连上、断开都是静默的，健康状况
	// 完全不可见。节点那条事件流就是在这种无声中每 20 秒断一次、推送一直
	// 没工作，直到有人专门去查才发现。浏览器的 EventSource 会自动重连，
	// 掩盖能力比节点端还强。
	//
	// 活不过两个心跳周期的连接不正常，直接记成警告。
	openedAt := time.Now()
	defer func() {
		lived := time.Since(openedAt)
		if lived < 2*sseHeartbeat {
			h.d.Log.Warn("事件流很快就断了",
				"存活", lived.Round(time.Millisecond).String(),
				"心跳间隔", sseHeartbeat.String())
			return
		}
		h.d.Log.Info("事件流已断开", "存活", lived.Round(time.Second).String())
	}()

	// 清掉这条连接的读写超时。
	//
	// http.Server 上配了 WriteTimeout/ReadTimeout（30s），那是给普通接口
	// 兜底的：一个写不完响应的请求不该一直占着连接。但 SSE 要挂几小时，
	// 被它管着就会稳定地在 30 秒断开。
	//
	// 用 ResponseController 只解除当前这条连接的限制，而不是把全局超时调大 ——
	// 后者会让所有接口一起失去保护，为了一个端点牺牲整个服务的兜底不划算。
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	// nginx 默认会缓冲上游响应，那会让事件攒在代理里发不出来。
	// 这个头是 nginx 专用的关闭开关，比要求每个部署都去改配置可靠。
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// 先发一条 retry，告诉浏览器断线后隔多久重连。
	// 不发的话默认是 3 秒，大量客户端同时断开时会形成一波重连风暴。
	fmt.Fprintf(w, "retry: 5000\n\n")
	flusher.Flush()

	// 心跳。中间的代理和负载均衡通常会掐掉一段时间没有数据的连接，
	// 而 SSE 在没有事件时确实一个字节都不会发。注释行（以冒号开头）
	// 不会触发客户端的任何事件处理，只是让链路上有数据流动。
	ping := time.NewTicker(sseHeartbeat)
	defer ping.Stop()

	var seq uint64
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			// 客户端断开或服务要关了
			return

		case ev, ok := <-events:
			if !ok {
				return
			}
			seq++
			if _, err := fmt.Fprint(w, realtime.FormatSSE(seq, ev)); err != nil {
				return
			}
			flusher.Flush()

		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
