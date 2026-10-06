package node

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/nodeagent/panel"
)

// 失败详情超过面板上限（2048 字节）会被整条拒收，面板就永远不知道为什么失败。
func TestSignedFailureDetailFitsPanelLimit(t *testing.T) {
	long := strings.Repeat("内核拒绝", 400) // 4800 字节
	got := reportDetail(long)
	if len(got) > maxReportDetailBytes || !utf8.ValidString(got) || !strings.HasPrefix(long, got) {
		t.Fatalf("截断后的详情不合规：%d 字节，utf8=%v", len(got), utf8.ValidString(got))
	}
	if reportDetail("短") != "短" {
		t.Fatal("短详情被改动")
	}
}

// 版本身份只认内容：面板每轮重签带来的 issued_at / expires_at / 签名变化不算新版本，
// 同一 release_id 换了内容算新版本。
func TestSignedConfigKeyIgnoresResigning(t *testing.T) {
	a := &panel.SignedConfig{
		ConfigContract: "aegis-node-effective-config-release-v1", ReleaseID: releaseBad, Generation: 2,
		ContentSHA256: "content-a", Hash: "content-a", Signature: "sig-1", IssuedAt: time.Unix(100, 0),
	}
	b := *a
	b.Signature, b.IssuedAt, b.ExpiresAt = "sig-2", time.Unix(200, 0), time.Unix(500, 0)
	if signedConfigKeyOf(a) != signedConfigKeyOf(&b) {
		t.Fatal("重签同一发布被当成了新版本")
	}
	c := *a
	c.ContentSHA256, c.Hash = "content-b", "content-b"
	if signedConfigKeyOf(a) == signedConfigKeyOf(&c) {
		t.Fatal("同一 release_id 换了内容却被当成同一版本")
	}
	legacy := &panel.SignedConfig{Version: 2, Hash: "content-a"}
	if signedConfigKeyOf(legacy) == signedConfigKeyOf(a) {
		t.Fatal("旧式签名配置与生效发布的身份混在了一起")
	}
}

// 面板明确拒收的不再补报；没送到的、面板暂时不可用的留给下一轮。
func TestReportSettled(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"成功":       {nil, true},
		"409 证据冲突": {&panel.StatusError{Code: http.StatusConflict}, true},
		"422 校验失败": {&panel.StatusError{Code: http.StatusUnprocessableEntity}, true},
		"包装后的 409": {errors.Join(errors.New("ctx"), &panel.StatusError{Code: http.StatusConflict}), true},
		"503 暂不可用": {&panel.StatusError{Code: http.StatusServiceUnavailable}, false},
		"429 限流":   {&panel.StatusError{Code: http.StatusTooManyRequests}, false},
		"408 超时":   {&panel.StatusError{Code: http.StatusRequestTimeout}, false},
		"传输错误":     {&url.Error{Op: "Post", URL: "https://panel.example.test", Err: errors.New("connection refused")}, false},
	} {
		if got := reportSettled(tc.err); got != tc.want {
			t.Errorf("%s：reportSettled = %v，期望 %v", name, got, tc.want)
		}
	}
}
