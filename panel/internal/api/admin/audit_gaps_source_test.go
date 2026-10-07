package admin

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 往外真发一条的测试接口在发之前留痕；解开明文来源 IP 的读取在解密之前留痕
// （审计台账 2.3 第 5、6 条）。写不进去就不发、不给。
func TestTestSendsAndSourceIPViewsAreAuditedFirst(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	for decl, action := range map[string]string{
		"handlers.testMailSettings": "sender.Send(",
		"handlers.testMailTemplate": "notify.NewSMTPSender(cfg).Send(",
		"handlers.testTelegram":     "sender.Send(",
		"handlers.testHook":         "h.d.Plugin.TestHook(",
	} {
		body := pkg.Decl(decl)
		record, act := strings.Index(body, "h.d.Ops.RecordTestSend("), strings.Index(body, action)
		if record < 0 || act < record {
			t.Errorf("%s must audit (RecordTestSend) before %s", decl, action)
		}
	}
	for decl, reveal := range map[string]string{
		"handlers.userProfile":   "h.decryptIP(",
		"handlers.accessLogList": "h.decryptIP(",
		"handlers.ipClusters":    "h.decryptIP(",
	} {
		body := pkg.Decl(decl)
		record, act := strings.Index(body, "h.d.Ops.RecordSourceIPView("), strings.Index(body, reveal)
		if record < 0 || act < record {
			t.Errorf("%s must audit (RecordSourceIPView) before decrypting source IPs", decl)
		}
	}
}
