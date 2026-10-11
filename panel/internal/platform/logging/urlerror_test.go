package logging

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 出站请求失败的错误不带 URL 的路径与查询串（Bot Token、商户密钥都在那里），
// 主机名与错误链保留。
func TestStripURLKeepsHostAndChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://user:pw@api.example.test/botSECRET/sendMessage?key=SECRET2", nil)
	_, err := http.DefaultClient.Do(req)
	if err == nil {
		t.Fatal("want an error from a cancelled request")
	}
	if !strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("precondition: net/http error should carry the URL, got %v", err)
	}
	got := StripURL(err)
	if s := got.Error(); strings.Contains(s, "SECRET") || strings.Contains(s, "pw@") || !strings.Contains(s, "https://api.example.test") {
		t.Fatalf("StripURL = %q", s)
	}
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("error chain lost: %v", got)
	}
	plain := fmt.Errorf("other")
	if StripURL(plain) != plain || StripURL(nil) != nil {
		t.Fatal("non-url errors must pass through unchanged")
	}
}
