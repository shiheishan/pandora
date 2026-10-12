package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// newUnit 构造不碰数据库的 service（store 为 nil，只能测不读写表的方法）。
func newUnit(t *testing.T, st *fakeSettings) (*Service, *fakeAudit, *fakeSender) {
	t.Helper()
	al, snd := &fakeAudit{}, &fakeSender{}
	svc, err := newService(nil, passTx{}, testKeys(t), st, al, nil, snd)
	if err != nil {
		t.Fatal(err)
	}
	return svc, al, snd
}

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed {
		t.Fatalf("want validation_failed, got %v", err)
	}
	return e.Fields
}

func TestMailSettingsFromNameFallsBackToSite(t *testing.T) {
	st := &fakeSettings{mail: configuredMail(), site: settingssvc.Site{Name: "站点"},
		reg: settingssvc.Registration{Mode: settingssvc.RegistrationOpen, EmailVerification: true}}
	st.mail.HasPassword = true
	svc, _, _ := newUnit(t, st)
	got, err := svc.MailSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.FromName != "站点" || !got.HasPassword || got.RegistrationMode != "open" || !got.EmailVerification {
		t.Fatalf("got %+v", got)
	}
}

func TestSaveMailSettingsOrderAndMerge(t *testing.T) {
	st := &fakeSettings{reg: settingssvc.Registration{Mode: settingssvc.RegistrationClosed}}
	svc, _, _ := newUnit(t, st)
	on := true
	if err := svc.SaveMailSettings(context.Background(), MailSettingsUpdate{
		Host: "h", Port: 465, Encryption: "ssl", FromAddress: "a@b.com", EmailVerification: &on,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.calls, ",") != "SetMail,SetRegistration" {
		t.Fatalf("calls %v", st.calls)
	}
	// 只给了邮箱验证，注册模式取当前值
	if st.setReg.Mode != settingssvc.RegistrationClosed || !st.setReg.EmailVerification {
		t.Fatalf("reg %+v", st.setReg)
	}
}

func TestSaveMailSettingsSkipsRegistration(t *testing.T) {
	st := &fakeSettings{}
	svc, _, _ := newUnit(t, st)
	if err := svc.SaveMailSettings(context.Background(), MailSettingsUpdate{Port: 465, Encryption: "ssl"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.calls, ",") != "SetMail" {
		t.Fatalf("calls %v", st.calls)
	}
}

func TestSaveMailSettingsRenamesPasswordField(t *testing.T) {
	st := &fakeSettings{setMailErr: errs.Invalid(map[string]string{"password": "重填"})}
	svc, _, _ := newUnit(t, st)
	f := fieldsOf(t, svc.SaveMailSettings(context.Background(), MailSettingsUpdate{Port: 465}))
	if f["smtp_password"] != "重填" || f["password"] != "" {
		t.Fatalf("fields %v", f)
	}
}

func TestSaveMailSettingsRejectsNUL(t *testing.T) {
	svc, _, _ := newUnit(t, &fakeSettings{})
	f := fieldsOf(t, svc.SaveMailSettings(context.Background(), MailSettingsUpdate{Host: "a\x00b"}))
	if f["smtp_host"] == "" {
		t.Fatalf("fields %v", f)
	}
}

func TestTestMailSettings(t *testing.T) {
	ctx := context.Background()

	svc, al, snd := newUnit(t, &fakeSettings{})
	if err := svc.TestMailSettings(ctx, "to@example.com"); err == nil || len(snd.sent) != 0 {
		t.Fatalf("未配置时应拒绝且不发：%v", err)
	}
	if f := fieldsOf(t, svc.TestMailSettings(ctx, " ")); f["to"] != "请填写收件地址" {
		t.Fatalf("fields %v", f)
	}
	if f := fieldsOf(t, svc.TestMailSettings(ctx, "Name <a@b.com>")); f["to"] != "邮箱格式不正确" {
		t.Fatalf("fields %v", f)
	}

	st := &fakeSettings{mail: configuredMail(), site: settingssvc.Site{Name: "站点"}}
	svc, al, snd = newUnit(t, st)
	if err := svc.TestMailSettings(ctx, " to@example.com "); err != nil {
		t.Fatal(err)
	}
	if len(al.events) != 1 || al.events[0].Action != "notify.test_sent" || al.events[0].Data["target"] != "to@example.com" {
		t.Fatalf("audit %+v", al.events)
	}
	if len(snd.sent) != 1 || snd.sent[0].To != "to@example.com" || snd.sent[0].Subject != "站点 邮件配置测试" {
		t.Fatalf("sent %+v", snd.sent)
	}

	// 写不进审计就不发
	al.err = errors.New("db down")
	snd.sent = nil
	if err := svc.TestMailSettings(ctx, "to@example.com"); err == nil || len(snd.sent) != 0 {
		t.Fatalf("审计失败仍发送：%v", err)
	}

	// 发信失败回 422，文案带阶段与原因
	al.err = nil
	snd.errs = []error{&SendError{Stage: "认证", Detail: "服务器回复 535 bad"}}
	err := svc.TestMailSettings(ctx, "to@example.com")
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed || e.Message != "发送失败：认证：服务器回复 535 bad" {
		t.Fatalf("err %v", err)
	}
}

func TestTestTemplateDraft(t *testing.T) {
	st := &fakeSettings{mail: configuredMail(), site: settingssvc.Site{Name: "站点"}}
	svc, al, snd := newUnit(t, st)
	ctx := context.Background()
	subject, body := "主题 {{code}}", "正文 {{site}}"

	got, err := svc.TestTemplate(ctx, TemplateTest{Code: TemplateEmailVerify, Channel: ChannelEmail,
		To: "to@example.com", Subject: &subject, Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	if got != "主题 "+sampleVars["code"] {
		t.Fatalf("subject %q", got)
	}
	if len(snd.sent) != 1 || snd.sent[0].Subject != "[测试] 主题 "+sampleVars["code"] || snd.sent[0].Body != "正文 "+sampleVars["site"] {
		t.Fatalf("sent %+v", snd.sent)
	}
	if len(al.events) != 1 || al.events[0].Data["draft"] != true {
		t.Fatalf("audit %+v", al.events)
	}

	bad := "{{balance}}"
	if f := fieldsOf(t, func() error {
		_, err := svc.TestTemplate(ctx, TemplateTest{Code: TemplateEmailVerify, Channel: ChannelEmail,
			To: "to@example.com", Subject: &subject, Body: &bad})
		return err
	}()); f["body"] == "" {
		t.Fatalf("fields %v", f)
	}

	if _, err := svc.TestTemplate(ctx, TemplateTest{Code: TemplateEmailVerify, Channel: "inapp", To: "to@example.com"}); err == nil {
		t.Fatal("站内信不能测试发送")
	}
	if _, err := svc.TestTemplate(ctx, TemplateTest{Code: "nope.code", Channel: ChannelEmail, To: "to@example.com"}); !isCode(err, errs.NotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestPreviewDraft(t *testing.T) {
	svc, _, _ := newUnit(t, &fakeSettings{})
	p, err := svc.PreviewDraft(context.Background(), TemplateEdit{Code: TemplateEmailVerify, Channel: ChannelEmail,
		Subject: "{{site}} {{zeta}}", Body: "{{code}} {{alpha}} {{zeta}}"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.UnknownVariables, ",") != "alpha,zeta" || !strings.HasPrefix(p.Subject, "{{site}}") {
		t.Fatalf("preview %+v", p)
	}
	p, err = svc.PreviewDraft(context.Background(), TemplateEdit{Code: TemplateEmailVerify, Channel: ChannelEmail,
		Subject: "{{site}}", Body: "{{code}}"})
	if err != nil || p.UnknownVariables == nil || len(p.UnknownVariables) != 0 || p.Subject != sampleVars["site"] {
		t.Fatalf("preview %+v err %v", p, err)
	}
	if f := fieldsOf(t, func() error {
		_, err := svc.PreviewDraft(context.Background(), TemplateEdit{Code: TemplateEmailVerify, Channel: ChannelEmail, Body: "{{if x}}{{end}}"})
		return err
	}()); f["body"] != errTemplateSyntax.Error() {
		t.Fatalf("fields %v", f)
	}
}

func isCode(err error, c errs.Code) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Code == c
}
