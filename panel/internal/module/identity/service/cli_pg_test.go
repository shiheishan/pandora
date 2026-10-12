package service

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestPGResetStaffPassword(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, uniqEmail(t, "cliu"), "Passw0rdLong1")
	if err := e.svc.ResetStaffPassword(ctx, user.ID, "NewPassw0rdLong9"); codeOf(err) != errs.BadRequest {
		t.Fatalf("普通用户应拒绝: %v", err)
	}
	if err := e.svc.ResetStaffPassword(ctx, uuid.New(), "NewPassw0rdLong9"); codeOf(err) != errs.NotFound {
		t.Fatalf("不存在: %v", err)
	}

	staff := e.mkUser(t, ctx, uniqEmail(t, "clis"), "Passw0rdLong1")
	e.staff.staff[staff.ID] = true
	if err := e.svc.ResetStaffPassword(ctx, staff.ID, "Passw0rd12"); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("后台口令规则: %v", err)
	}
	e.revoker.calls = nil
	if err := e.svc.ResetStaffPassword(ctx, staff.ID, "NewPassw0rdLong9"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, authn.AudienceAdmin, staff.Email, "Passw0rdLong1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧口令: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, authn.AudienceAdmin, staff.Email, "NewPassw0rdLong9"); err != nil {
		t.Fatal(err)
	}
	if len(e.revoker.calls) != 1 || e.revoker.calls[0].user != staff.ID {
		t.Fatalf("revoke %+v", e.revoker.calls)
	}
	if e.auditCount(ctx, ActionPasswordResetByCLI, staff.ID) != 1 {
		t.Fatal("audit")
	}
}
