package kernel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNativeRealityXHTTPXrayClientInterop(t *testing.T) {
	env := startRealityVLESS(t, map[string]any{"network": "xhttp", "path": "/xhttp"})
	adapter, id := env.adapter, env.id
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	realityConn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("xray REALITY client handshake: %v", err)
	}
	defer realityConn.Close()
	payload := []byte("reality-xhttp-native")
	header := append([]byte{vlessVersion}, id[:]...)
	header = append(header, 0, vlessTCP, 0, 0xbb, 1, 127, 0, 0, 1)
	bodyPayload := append(header, payload...)
	// 节点不写 mode（缺省 auto）。Xray / mihomo 在 REALITY 上的 auto 选 stream-one：
	// 不带会话、直接 POST 基础路径。10-08 真节点测试里这种请求被回 400。
	if _, err := fmt.Fprintf(realityConn, "POST /xhttp/ HTTP/1.1\r\nHost: example.com\r\nContent-Length: %d\r\nContent-Type: application/octet-stream\r\nConnection: close\r\n\r\n", len(bodyPayload)); err != nil {
		t.Fatal(err)
	}
	if _, err := realityConn.Write(bodyPayload); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(realityConn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != http.StatusOK || len(body) != len(payload)+2 || body[0] != vlessVersion || body[1] != 0 || string(body[2:]) != string(payload) {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 941 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}

	// REALITY clients select HTTP/2 for XHTTP. The same Pandora listener must
	// accept an h2 prior-knowledge session after the REALITY handshake instead
	// of only passing an HTTP/1.1 smoke test.
	h2Transport := h2cTestTransport(func(dialCtx context.Context, _, _ string) (net.Conn, error) { return env.dial(dialCtx) })
	h2Client := &http.Client{Transport: h2Transport, Timeout: 5 * time.Second}
	h2Payload := []byte("reality-xhttp-h2-native")
	h2Header := append([]byte{vlessVersion}, id[:]...)
	h2Header = append(h2Header, 0, vlessTCP, 0, 0xbb, 1, 127, 0, 0, 1)
	h2Body := append(h2Header, h2Payload...)
	h2Req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.com/xhttp/", bytes.NewReader(h2Body))
	if err != nil {
		t.Fatal(err)
	}
	h2Req.Host = "example.com"
	h2Req.Header.Set("Content-Type", "application/octet-stream")
	h2Resp, err := h2Client.Do(h2Req)
	if err != nil {
		t.Fatalf("xray REALITY H2 XHTTP request: %v", err)
	}
	h2ResponseBody, readErr := io.ReadAll(h2Resp.Body)
	_ = h2Resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if h2Resp.StatusCode != http.StatusOK || len(h2ResponseBody) != len(h2Payload)+2 || h2ResponseBody[0] != vlessVersion || h2ResponseBody[1] != 0 || string(h2ResponseBody[2:]) != string(h2Payload) {
		t.Fatalf("h2 status=%d body=%q", h2Resp.StatusCode, h2ResponseBody)
	}
}

func uuidString(id [16]byte) string { return uuid.UUID(id).String() }
