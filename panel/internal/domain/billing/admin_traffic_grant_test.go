package billing

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestValidateAdminTrafficGrant(t *testing.T) {
	for _, tc := range []struct {
		bytes  int64
		reason string
		field  string
	}{
		{0, "补偿线路故障", "bytes"},
		{-1, "补偿线路故障", "bytes"},
		{maxAdminTrafficGrantBytes + 1, "补偿线路故障", "bytes"},
		{1 << 30, "补偿", "reason"},
		{1 << 30, strings.Repeat("长", 501), "reason"},
		{1 << 30, "  补偿线路故障  ", ""},
		{maxAdminTrafficGrantBytes, "补偿线路故障", ""},
	} {
		reason, err := validateAdminTrafficGrant(tc.bytes, tc.reason)
		if tc.field == "" {
			if err != nil || reason != strings.TrimSpace(tc.reason) {
				t.Fatalf("bytes=%d reason=%q err=%v got=%q", tc.bytes, tc.reason, err, reason)
			}
			continue
		}
		var he *httpx.Error
		if !errors.As(err, &he) || he.Fields[tc.field] == "" {
			t.Fatalf("bytes=%d reason=%q err=%v, want field %s", tc.bytes, tc.reason, err, tc.field)
		}
	}
}

// 发放、审计、幂等完成在同一个事务里，且走与礼品卡同一个 GrantTrafficPackTx（来源 admin）；
// 节点通知在事务提交之后发。
func TestAdminTrafficGrantSourceContract(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("Service.GrantTrafficPackAsAdmin")
	grant := strings.Index(body, `GrantTrafficPackTx(ctx, tx, tenantID, out.UserID, "admin"`)
	auditAt := strings.Index(body, "audit.Write(ctx, tx")
	complete := strings.Index(body, "middleware.CompleteSuccessJSONInTx(ctx, tx, in.Claim, prepared)")
	notify := strings.Index(body, "s.notifyUsersChanged(ctx, tenantID)")
	if grant < 0 || auditAt < grant || complete < auditAt || notify < complete {
		t.Fatalf("grant=%d audit=%d complete=%d notify=%d: grant, audit and idempotency must share one transaction, notify after commit",
			grant, auditAt, complete, notify)
	}
}
