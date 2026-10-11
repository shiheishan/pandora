package plugin

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

// 钩子地址常把令牌放在路径里：投递失败的原因写进投递记录，只留主机名。
func TestHookSendErrorOmitsEndpointPath(t *testing.T) {
	s := &Service{client: &http.Client{Transport: failingTransport{}}}
	_, err := s.send(context.Background(), dueDelivery{
		endpoint: "https://hooks.example.test/services/T0/SECRETPATH?token=SECRETQ", timeoutMS: 1000,
		payload: []byte(`{}`), event: "x", id: "d1",
	}, "")
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "hooks.example.test") {
		t.Fatalf("error = %q", err)
	}
}
