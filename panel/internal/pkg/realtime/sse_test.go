package realtime

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// readUntil 读到累计内容里出现 needle 为止，返回累计读到的全部内容。
func readUntil(t *testing.T, body io.Reader, needle string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var buf strings.Builder
	for time.Now().Before(deadline) {
		b := make([]byte, 256)
		n, err := body.Read(b)
		if n > 0 {
			buf.Write(b[:n])
			if strings.Contains(buf.String(), needle) {
				return buf.String()
			}
		}
		if err != nil {
			if err == io.EOF {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			t.Fatalf("read: %v; got %q", err, buf.String())
		}
	}
	t.Fatalf("timeout waiting for %q; got %q", needle, buf.String())
	return ""
}

func TestStreamRetryAndEventFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHub(nil, nil, 100)
	owner := uuid.New()
	ch := ChannelPublic()

	r := gin.New()
	r.GET("/events", func(c *gin.Context) {
		Stream(c, h, owner, []string{ch}, 0)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	readUntil(t, resp.Body, "retry: 5000")

	h.dispatch(ch, Event{
		Topic:   TopicTickets,
		Payload: map[string]any{"table": "tickets", "op": OpUpdate, "id": "abc"},
	})

	frame := readUntil(t, resp.Body, `"table":"tickets"`)
	for _, want := range []string{"id: 1\n", "event: tickets.changed\n", "data: {"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("帧里缺 %q：%q", want, frame)
		}
	}
}

func TestStreamSessionRevokedEnds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, rdb := testRedis(t)
	h := NewHub(rdb, nil, 100)
	t.Cleanup(h.Close)
	waitReady(t, h)
	owner := uuid.New()
	sid := "test-sid-1"

	r := gin.New()
	r.GET("/events", func(c *gin.Context) {
		Stream(c, h, owner, []string{ChannelSession(sid)}, 0)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	readUntil(t, resp.Body, "retry: 5000")

	if err := rdb.Publish(context.Background(), ChannelSession(sid), "revoked").Err(); err != nil {
		t.Fatal(err)
	}

	// 连接应在服务端 return 后结束
	br := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := br.ReadByte()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	t.Fatal("connection still open after session revoked")
}

// 到了最长存活时间服务端主动结束，客户端带新令牌重连。
func TestStreamEndsAtMaxLife(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHub(nil, nil, 100)
	r := gin.New()
	r.GET("/events", func(c *gin.Context) {
		Stream(c, h, uuid.New(), []string{ChannelPublic()}, 200*time.Millisecond)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// 客户端超时兜底：服务端到点不结束，ReadAll 会报超时
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("到点后连接没结束: %v", err)
	}
	if h.Count() != 0 {
		t.Fatalf("连接结束后仍登记 %d 条", h.Count())
	}
}
