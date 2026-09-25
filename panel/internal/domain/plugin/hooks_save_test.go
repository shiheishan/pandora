package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// R93：超时与重试次数越界以前只靠 00051 的 CHECK 约束兜底，落到数据库才
// 失败，回的是 500。现在保存前拦成 422，字段键就是请求字段名。
//
// 每个用例都故意留空 name：校验一定在碰数据库之前失败，服务可以不带连接池，
// 同时能看出合法的边界值没有被误报。
func TestSaveHookRejectsOutOfRangeTimeoutAndAttempts(t *testing.T) {
	s := New(nil, nil, true)
	cases := []struct {
		name                   string
		timeoutMS, maxAttempts int
		wantTimeout, wantTries bool
	}{
		{"零值取默认", 0, 0, false, false},
		{"下边界", 500, 1, false, false},
		{"上边界", 30000, 10, false, false},
		{"超时太短", 499, 5, true, false},
		{"超时太长", 30001, 5, true, false},
		{"超时为负", -1, 5, true, false},
		{"次数太多", 5000, 11, false, true},
		{"次数为负", 5000, -1, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SaveHook(context.Background(), "tenant", SaveHookInput{
				Code: "hook-a", EndpointURL: "https://hooks.example.com/in",
				TimeoutMS: tc.timeoutMS, MaxAttempts: tc.maxAttempts,
			})
			var he *httpx.Error
			if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
				t.Fatalf("want 422, got %v", err)
			}
			if _, ok := he.Fields["name"]; !ok {
				t.Fatalf("name field missing: %v", he.Fields)
			}
			if _, got := he.Fields["timeout_ms"]; got != tc.wantTimeout {
				t.Fatalf("timeout_ms flagged=%v want %v: %v", got, tc.wantTimeout, he.Fields)
			}
			if _, got := he.Fields["max_attempts"]; got != tc.wantTries {
				t.Fatalf("max_attempts flagged=%v want %v: %v", got, tc.wantTries, he.Fields)
			}
		})
	}
}
