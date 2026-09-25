package identity

import "testing"

// R101：原因可选，空串不在审计摘要里占位，给了才记。
func TestResetAuditDigestReasonOptional(t *testing.T) {
	d := resetAuditDigest("u@example.test", "")
	if _, ok := d["reason"]; ok || d["target_email"] != "u@example.test" || d["sessions_revoked"] != true {
		t.Fatalf("digest without reason=%v", d)
	}
	if d := resetAuditDigest("u@example.test", "用户来电找回"); d["reason"] != "用户来电找回" {
		t.Fatalf("digest with reason=%v", d)
	}
}
