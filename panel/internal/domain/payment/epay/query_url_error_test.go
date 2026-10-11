package epay

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

// 查询接口把明文商户密钥放在查询串里（key=）：请求失败时错误不能带上它，错误会进日志。
func TestQueryPaymentTransportErrorOmitsKey(t *testing.T) {
	p := testProvider(t)
	p.client = &http.Client{Transport: failingTransport{}}
	_, err := p.QueryPayment(context.Background(), "ORDER1")
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), "TESTKEY") || !strings.Contains(err.Error(), "pay.example.com") {
		t.Fatalf("error = %q", err)
	}
}
