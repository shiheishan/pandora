package realtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// sseHeartbeat 是没有事件时的保活间隔；同时是「连接活得够不够久」的判据。
const sseHeartbeat = 25 * time.Second

// Stream 处理一条 SSE 长连接。
//
// maxLife 是连接最长存活时间（handler 传访问令牌的有效期）：鉴权只在建连时做一次，
// 到点主动结束，客户端带新令牌重连，空闲过期、停用之类不发吊销信号的失效也能及时生效。
// 0 表示不限。
func Stream(c *gin.Context, hub *Hub, owner uuid.UUID, channels []string, maxLife time.Duration) {
	if hub == nil {
		httpx.Fail(c, errs.New(errs.Unavailable))
		return
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		httpx.Fail(c, errs.Wrap(errs.Internal, fmt.Errorf("响应不支持流式输出")))
		return
	}

	sub, err := hub.Subscribe(owner, channels)
	if err != nil {
		switch {
		case errors.Is(err, ErrTooManyUserStreams):
			httpx.Fail(c, errs.New(errs.RateLimited))
		case errors.Is(err, ErrTooManyStreams):
			httpx.Fail(c, errs.New(errs.Unavailable))
		default:
			httpx.Fail(c, errs.Wrap(errs.Internal, err))
		}
		return
	}
	defer sub.Close()

	openedAt := time.Now()
	defer func() {
		lived := time.Since(openedAt)
		l := plog.From(c.Request.Context())
		if lived < 2*sseHeartbeat {
			l.Warn("事件流很快就断了",
				zap.String("存活", lived.Round(time.Millisecond).String()),
				zap.String("心跳间隔", sseHeartbeat.String()))
			return
		}
		l.Info("事件流已断开", zap.String("存活", lived.Round(time.Second).String()))
	}()

	rc := http.NewResponseController(c.Writer)
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	fmt.Fprintf(c.Writer, "retry: 5000\n\n")
	flusher.Flush()

	ping := time.NewTicker(sseHeartbeat)
	defer ping.Stop()

	var expire <-chan time.Time
	if maxLife > 0 {
		t := time.NewTimer(maxLife)
		defer t.Stop()
		expire = t.C
	}

	var seq uint64
	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Revoked:
			return
		case <-expire:
			return
		case ev := <-sub.Events:
			seq++
			if _, err := fmt.Fprint(c.Writer, formatSSE(seq, ev)); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func formatSSE(id uint64, ev Event) string {
	body, err := json.Marshal(ev.Payload)
	if err != nil {
		body = []byte("{}")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "id: %d\n", id)
	fmt.Fprintf(&b, "event: %s\n", ev.Topic)
	fmt.Fprintf(&b, "data: %s\n\n", body)
	return b.String()
}
