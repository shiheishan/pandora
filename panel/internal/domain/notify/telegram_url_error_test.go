package notify

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

// 发送失败时错误里不能带 Bot Token：URL 是 /bot<token>/sendMessage，错误会进日志与发送记录。
func TestTelegramSendErrorOmitsBotToken(t *testing.T) {
	s := &TelegramSender{token: "123456:SECRETTOKEN", client: &http.Client{Transport: failingTransport{}}}
	err := s.Send(context.Background(), "42", "", "hi")
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") || !strings.Contains(err.Error(), "api.telegram.org") {
		t.Fatalf("error = %q", err)
	}
}
