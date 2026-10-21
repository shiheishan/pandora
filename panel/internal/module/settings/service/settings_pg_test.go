package service

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/settings/repo"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

func newPG(t *testing.T) (*Service, *repo.Repo, context.Context, *auditLog) {
	d := testkit.DB(t)
	r := repo.New(d)
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	al := &auditLog{}
	svc := newService(r, tx.NewManager(d), sealer, nil, al)
	ctx := testkit.TxCtx(t, d)
	return svc, r, ctx, al
}

func TestSeedSwitchesMatchMigration(t *testing.T) {
	_, r, ctx, _ := newPG(t)
	rows, err := r.ListSwitches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(SeedSwitches) {
		t.Fatalf("count %d want %d", len(rows), len(SeedSwitches))
	}
	got := make(map[string]bool, len(rows))
	for _, sw := range rows {
		if !sw.Enabled {
			t.Fatalf("%s not enabled", sw.Code)
		}
		got[sw.Code] = sw.Essential
	}
	for code, essential := range SeedSwitches {
		if got[code] != essential {
			t.Fatalf("%s essential=%v want %v", code, got[code], essential)
		}
	}
	if len(got) != len(SeedSwitches) {
		t.Fatalf("distinct codes %d", len(got))
	}
}

func TestSiteDefaultsAndRoundTrip(t *testing.T) {
	svc, _, ctx, al := newPG(t)
	site, err := svc.Site(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantDef := Site{Name: "Pandora", Timezone: "Asia/Shanghai"}
	if site != wantDef {
		t.Fatalf("default %+v want %+v", site, wantDef)
	}

	want := Site{Name: "示例站", Timezone: "Asia/Tokyo"}
	if err := svc.SetSite(ctx, fullSite(want)); err != nil {
		t.Fatal(err)
	}
	site, err = svc.Site(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if site != want {
		t.Fatalf("after set %+v want %+v", site, want)
	}
	if len(al.entries) != 1 || al.entries[0].action != auditSiteChanged {
		t.Fatalf("audit %+v", al.entries)
	}

	if err := svc.SetSite(ctx, fullSite(want)); err != nil {
		t.Fatal(err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("same-value set should not audit, count %d", len(al.entries))
	}

	err = svc.SetSite(ctx, SiteUpdate{Timezone: ptr("Local")})
	if errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("Local tz: %v", err)
	}
}

// 只改时区不会把别人刚改的站点名写回旧值。
func TestSetSitePartialKeepsOtherFields(t *testing.T) {
	svc, _, ctx, _ := newPG(t)
	if err := svc.SetSite(ctx, SiteUpdate{Name: ptr("别处改的名")}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSite(ctx, SiteUpdate{Timezone: ptr("Asia/Tokyo")}); err != nil {
		t.Fatal(err)
	}
	site, err := svc.Site(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if site.Name != "别处改的名" || site.Timezone != "Asia/Tokyo" {
		t.Fatalf("site %+v", site)
	}
}

func fullSite(v Site) SiteUpdate {
	return SiteUpdate{Name: &v.Name, Timezone: &v.Timezone}
}

func TestMailPasswordSealRoundTrip(t *testing.T) {
	svc, r, ctx, al := newPG(t)
	const secret = "s3cret-pass"

	ok, err := svc.MailConfigured(ctx)
	if err != nil || ok {
		t.Fatalf("before mail: ok=%v err=%v", ok, err)
	}

	pwd := secret
	if err := svc.SetMail(ctx, MailUpdate{
		Host:        "smtp.example.test",
		Port:        587,
		Encryption:  MailEncryptionTLS,
		FromAddress: "noreply@example.test",
		Password:    &pwd,
	}); err != nil {
		t.Fatal(err)
	}

	ok, err = svc.MailConfigured(ctx)
	if err != nil || !ok {
		t.Fatalf("after mail: ok=%v err=%v", ok, err)
	}

	m, err := svc.Mail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasPassword || m.Password != secret || !m.Configured() {
		t.Fatalf("mail %+v", m)
	}

	vals, err := r.Values(ctx, []string{keyMailPassword})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(vals[keyMailPassword]), secret) {
		t.Fatal("stored value must not contain plaintext password")
	}
	for _, e := range al.entries {
		if strings.Contains(fmt.Sprint(e.data), secret) {
			t.Fatalf("audit leaks password: %v", e.data)
		}
	}

	if err := svc.SetMail(ctx, MailUpdate{
		Host:        m.Host,
		Port:        m.Port,
		Encryption:  m.Encryption,
		FromAddress: m.FromAddress,
		Password:    nil,
	}); err != nil {
		t.Fatal(err)
	}
	m2, err := svc.Mail(ctx)
	if err != nil || !m2.HasPassword || m2.Password != secret {
		t.Fatalf("nil password keep: %+v err=%v", m2, err)
	}

	// 换服务器不给新口令：拒绝，旧口令不会被带去新服务器
	err = svc.SetMail(ctx, MailUpdate{
		Host:        "smtp.attacker.example.test",
		Port:        m.Port,
		Encryption:  m.Encryption,
		FromAddress: m.FromAddress,
	})
	if e := errs.As(err); e.Code != errs.ValidationFailed || e.Fields["password"] == "" {
		t.Fatalf("host change without password: %v", err)
	}

	empty := ""
	if err := svc.SetMail(ctx, MailUpdate{
		Host:        m.Host,
		Port:        m.Port,
		Encryption:  m.Encryption,
		FromAddress: m.FromAddress,
		Password:    &empty,
	}); err != nil {
		t.Fatal(err)
	}
	m3, err := svc.Mail(ctx)
	if err != nil || m3.HasPassword {
		t.Fatalf("clear password: %+v err=%v", m3, err)
	}
}

func TestMailValidation(t *testing.T) {
	svc, _, ctx, _ := newPG(t)

	err := svc.SetMail(ctx, MailUpdate{Port: 0})
	e := errs.As(err)
	if e.Code != errs.ValidationFailed || e.Fields["smtp_port"] == "" {
		t.Fatalf("port 0: code=%s fields=%v", e.Code, e.Fields)
	}

	err = svc.SetMail(ctx, MailUpdate{Encryption: "starttls"})
	e = errs.As(err)
	if e.Code != errs.ValidationFailed || e.Fields["encryption"] == "" {
		t.Fatalf("encryption: code=%s fields=%v", e.Code, e.Fields)
	}

	err = svc.SetMail(ctx, MailUpdate{FromAddress: "not-an-email"})
	e = errs.As(err)
	if e.Code != errs.ValidationFailed || e.Fields["from_address"] == "" {
		t.Fatalf("from: code=%s fields=%v", e.Code, e.Fields)
	}
}

func setMailOK(t *testing.T, svc *Service, ctx context.Context) {
	pwd := "smtp-pass"
	if err := svc.SetMail(ctx, MailUpdate{
		Host:        "smtp.example.test",
		Port:        587,
		Encryption:  MailEncryptionTLS,
		FromAddress: "noreply@example.test",
		Password:    &pwd,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationPolicy(t *testing.T) {
	svc, _, ctx, _ := newPG(t)

	reg, err := svc.Registration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Mode != RegistrationOpen || reg.EmailVerification {
		t.Fatalf("default %+v", reg)
	}
	pol, err := svc.RegistrationPolicy(ctx)
	if err != nil || pol.Mode != RegistrationOpen {
		t.Fatalf("policy %+v err=%v", pol, err)
	}

	if err := svc.SetSwitch(ctx, SwitchAuthRegistration, false, "演练"); err != nil {
		t.Fatal(err)
	}
	pol, err = svc.RegistrationPolicy(ctx)
	if err != nil || pol.Mode != RegistrationClosed {
		t.Fatalf("after switch off policy %+v err=%v", pol, err)
	}
	reg, err = svc.Registration(ctx)
	if err != nil || reg.Mode != RegistrationOpen {
		t.Fatalf("registration row still %+v err=%v", reg, err)
	}

	err = svc.SetRegistration(ctx, Registration{Mode: RegistrationOpen, EmailVerification: true})
	e := errs.As(err)
	if e.Code != errs.ValidationFailed || e.Fields["email_verification"] == "" {
		t.Fatalf("no smtp: code=%s fields=%v", e.Code, e.Fields)
	}

	setMailOK(t, svc, ctx)
	if err := svc.SetRegistration(ctx, Registration{Mode: RegistrationOpen, EmailVerification: true}); err != nil {
		t.Fatal(err)
	}
	reg, err = svc.Registration(ctx)
	if err != nil || !reg.EmailVerification {
		t.Fatalf("after smtp %+v err=%v", reg, err)
	}
}

func TestSetSwitchConstraints(t *testing.T) {
	svc, r, ctx, _ := newPG(t)

	err := svc.SetSwitch(ctx, SwitchAuthLogin, false, "x")
	if errs.As(err).Code != errs.Conflict {
		t.Fatalf("essential close: %v", err)
	}
	err = svc.SetSwitch(ctx, "nope", true, "")
	if errs.As(err).Code != errs.NotFound {
		t.Fatalf("unknown switch: %v", err)
	}

	err = r.UpdateSwitch(ctx, SwitchAuthLogin, false, strPtr("x"), nil)
	if err == nil {
		t.Fatal("expected DB CHECK to reject disabling essential switch")
	}
}

func TestSwitchesListShape(t *testing.T) {
	svc, _, ctx, _ := newPG(t)
	rows, err := svc.Switches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(SeedSwitches) {
		t.Fatalf("count %d want %d", len(rows), len(SeedSwitches))
	}
	wantFirst := []string{SwitchAuthLogin, SwitchClientConfigSync, SwitchSubscriptionRenewal}
	for i, code := range wantFirst {
		if rows[i].Code != code || !rows[i].Essential {
			t.Fatalf("row[%d] %+v want essential %s", i, rows[i], code)
		}
	}
}

func ptr(s string) *string { return &s }

func TestSetMailRejectsControlCharsInFromName(t *testing.T) {
	svc, _, ctx, _ := newPG(t)
	err := svc.SetMail(ctx, MailUpdate{
		Host:        "smtp.example.test",
		Port:        465,
		Encryption:  MailEncryptionSSL,
		FromAddress: "noreply@example.test",
		FromName:    "名字\r\nBcc: x@example.test",
	})
	if e := errs.As(err); e.Code != errs.ValidationFailed || e.Fields["from_name"] == "" {
		t.Fatalf("err %v", err)
	}
}

// 事务没提交（测试事务最后回滚）时不推开关变更。
func TestSetSwitchUncommittedDoesNotPublish(t *testing.T) {
	svc, _, ctx, _ := newPG(t)
	pub, spy := newRTSpy(t, realtime.ChannelAdmin(), realtime.ChannelPublic())
	svc.UseRealtime(pub)
	if err := svc.SetSwitch(ctx, SwitchAuthRegistration, false, "演练"); err != nil {
		t.Fatal(err)
	}
	spy.expectNone(t, 300*time.Millisecond)
}
