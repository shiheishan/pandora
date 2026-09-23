package admin

// 管理端 SSE。
//
// 与用户侧的区别只有订阅范围：管理员订阅的是 admin 频道，
// 那里汇集了整个租户的变更。鉴权由 admin 域的令牌保证 ——
// 这个端点挂在需要登录的分组内，拿用户令牌访问不到。

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

func (h *handlers) events(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	if h.d.Realtime == nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(fmt.Errorf("实时推送未启用")))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(fmt.Errorf("响应不支持流式输出")))
		return
	}

	events, unsubscribe := h.d.Realtime.Subscribe([]string{
		realtime.ChannelAdmin(p.TenantID),
	})
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
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "retry: 5000\n\n")
	flusher.Flush()

	ping := time.NewTicker(sseHeartbeat)
	defer ping.Stop()

	var seq uint64
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
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
