package service

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/module/notify/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

type pgEnv struct {
	db     *db.DB
	svc    *Service
	repo   *repo.Repo
	ctx    context.Context
	st     *fakeSettings
	audit  *fakeAudit
	sender *fakeSender
	queue  *fakeQueue
	keys   *crypto.Keys
	rows   func(t *testing.T) []deliveryRow
}

// deliveryRow 是测试直接查出的投递状态。
type deliveryRow struct {
	ID           string
	TemplateCode string
	Status       string
	Attempts     int
	ErrorMessage *string
	HasPayload   bool
}

// newPG 每个用例一个回滚事务；service 的事务、认领与状态更新都加入它，提交后钩子不执行。
func newPG(t *testing.T) *pgEnv {
	d := testkit.DB(t)
	r := repo.New(d)
	e := &pgEnv{
		db:     d,
		repo:   r,
		ctx:    testkit.TxCtx(t, d),
		st:     &fakeSettings{mail: configuredMail(), site: settingssvc.Site{Name: "测试站"}},
		audit:  &fakeAudit{},
		sender: &fakeSender{},
		queue:  &fakeQueue{},
		keys:   testKeys(t),
	}
	svc, err := newService(r, tx.NewManager(d), e.keys, e.st, e.audit, e.queue, e.sender)
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.rows = func(t *testing.T) []deliveryRow {
		t.Helper()
		var out []deliveryRow
		err := d.Conn(e.ctx).Raw(`SELECT id::text AS id, template_code, status, attempts, error_message,
		        payload_enc IS NOT NULL AS has_payload
		   FROM notification_deliveries ORDER BY created_at, id`).Scan(&out).Error
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	return e
}

func (e *pgEnv) send(t *testing.T, code string) {
	t.Helper()
	if err := e.svc.SendToAddress(e.ctx, code, "user@example.com",
		map[string]string{"code": "123456", "minutes": "10"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSeedTemplatesMatchRegistry(t *testing.T) {
	e := newPG(t)
	rows, err := e.repo.ListTemplates(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(templateDefs) {
		t.Fatalf("种子 %d 行，登记表 %d 个", len(rows), len(templateDefs))
	}
	for _, def := range templateDefs {
		row, ok, err := e.repo.GetTemplate(e.ctx, def.Code, def.Channel)
		if err != nil || !ok {
			t.Fatalf("%s/%s 缺种子: %v", def.Code, def.Channel, err)
		}
		if row.Subject != def.Subject || row.Body != def.Body || row.Version != 1 {
			t.Fatalf("%s/%s 种子与 registry.go 不一致", def.Code, def.Channel)
		}
		// 默认内容本身必须能通过保存时的校验
		if _, _, err := validateTemplateContent(def.Subject, def.Body, def.Allowed); err != nil {
			t.Fatalf("%s 默认内容校验失败: %v", def.Code, err)
		}
	}
}

func TestSendToAddressWritesSealedDelivery(t *testing.T) {
	e := newPG(t)
	err := e.svc.SendToAddress(e.ctx, TemplatePasswordReset, " User@Example.com ",
		map[string]string{"code": "123456", "minutes": "10", "extra": "丢掉"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := e.repo.ClaimDeliveries(e.ctx, ChannelEmail, 10, time.Minute)
	if err != nil || len(ds) != 1 {
		t.Fatalf("claim %d %v", len(ds), err)
	}
	d := ds[0]
	if d.TemplateCode != TemplatePasswordReset || d.UserID != nil || d.Status != model.StatusQueued || d.MaxAttempts != maxAttempts {
		t.Fatalf("row %+v", d)
	}
	if !bytes.Equal(d.RecipientHash, e.keys.HMAC(crypto.InfoHMACNotify, []byte("user@example.com"))) {
		t.Fatal("recipient_hash 不是小写地址的 HMAC")
	}
	raw, err := e.svc.sealer.Open(d.PayloadEnc, deliveryAAD(d.ID))
	if err != nil {
		t.Fatal(err)
	}
	var p deliveryPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.To != "User@Example.com" || p.Vars["code"] != "123456" || p.Vars["site"] != "测试站" || p.Vars["extra"] != "" {
		t.Fatalf("payload %+v", p)
	}
	// 密文绑定到行 id，挪到别的行解不开
	if _, err := e.svc.sealer.Open(d.PayloadEnc, []byte("notification_deliveries.payload:other")); err == nil {
		t.Fatal("AAD 未生效")
	}
	// 在事务里：提交后才入队
	if len(e.queue.tasks) != 0 {
		t.Fatal("提交前不应入队")
	}
}

// 坏地址不报错也不入队：报错会让找回、注册的 start 对存在与不存在的账号响应不同。
func TestSendToAddressSkipsBadAddress(t *testing.T) {
	e := newPG(t)
	if err := e.svc.SendToAddress(e.ctx, TemplateEmailVerify, "a@b.com\r\nBcc: c@d.com", nil, nil); err != nil {
		t.Fatalf("err %v", err)
	}
	if rows := e.rows(t); len(rows) != 0 {
		t.Fatalf("坏地址不应入队: %+v", rows)
	}
	if err := e.svc.SendToAddress(e.ctx, "nope.code", "a@b.com", nil, nil); !isCode(err, errs.Internal) {
		t.Fatalf("err %v", err)
	}
}

func TestDispatchSendsAndScrubs(t *testing.T) {
	e := newPG(t)
	e.send(t, TemplateEmailVerify)
	n, err := e.svc.Dispatch(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	if len(e.sender.sent) != 1 {
		t.Fatalf("sent %d", len(e.sender.sent))
	}
	m := e.sender.sent[0]
	if m.To != "user@example.com" || m.Subject != "【测试站】注册验证码 123456" {
		t.Fatalf("message %+v", m)
	}
	if e.sender.cfgs[0].FromName != "测试站" {
		t.Fatalf("发件人名应回落到站点名：%+v", e.sender.cfgs[0])
	}
	rows := e.rows(t)
	if len(rows) != 1 || rows[0].Status != model.StatusSent || rows[0].HasPayload || rows[0].Attempts != 1 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestDispatchRetryAndPermanentFailure(t *testing.T) {
	e := newPG(t)
	e.send(t, TemplateEmailVerify)
	e.send(t, TemplatePasswordReset)
	e.sender.errs = []error{
		&SendError{Stage: "连接", Detail: "超时"},
		&SendError{Stage: "收件人", Detail: "服务器回复 550 <地址>", Permanent: true},
	}
	if n, err := e.svc.Dispatch(e.ctx); err != nil || n != 2 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	rows := e.rows(t)
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	retry, failed := rows[0], rows[1]
	if retry.Status != model.StatusQueued || retry.Attempts != 1 || !retry.HasPayload ||
		retry.ErrorMessage == nil || *retry.ErrorMessage != "连接：超时" {
		t.Fatalf("retry %+v", retry)
	}
	if failed.Status != model.StatusFailed || failed.Attempts != 1 || failed.HasPayload {
		t.Fatalf("failed %+v", failed)
	}
	// 退避中的行本轮不会再被认领
	if n, err := e.svc.Dispatch(e.ctx); err != nil || n != 0 {
		t.Fatalf("second dispatch %d %v", n, err)
	}
}

func TestDispatchSwitchOffKeepsQueue(t *testing.T) {
	e := newPG(t)
	e.send(t, TemplateEmailVerify)
	e.st.emailOff = true
	if n, err := e.svc.Dispatch(e.ctx); err != nil || n != 0 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	rows := e.rows(t)
	if len(e.sender.sent) != 0 || len(rows) != 1 || rows[0].Status != model.StatusQueued || rows[0].Attempts != 0 {
		t.Fatalf("开关关闭时应原样留在队列：%+v", rows)
	}
}

func TestDispatchNotConfiguredSuppresses(t *testing.T) {
	e := newPG(t)
	e.send(t, TemplateEmailVerify)
	e.st.mail = settingssvc.Mail{Port: 465}
	if _, err := e.svc.Dispatch(e.ctx); err != nil {
		t.Fatal(err)
	}
	rows := e.rows(t)
	if len(e.sender.sent) != 0 || rows[0].Status != model.StatusSuppressed || rows[0].HasPayload ||
		rows[0].ErrorMessage == nil || *rows[0].ErrorMessage != "渠道未配置" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestSaveAndResetTemplate(t *testing.T) {
	e := newPG(t)
	v, err := e.svc.SaveTemplate(e.ctx, TemplateEdit{Code: TemplatePasswordReset, Channel: ChannelEmail,
		Subject: " 新主题 {{code}} ", Body: "新正文 {{site}} {{minutes}}"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 2 || v.Subject != "新主题 {{code}}" || v.IsDefault || !v.HasDefault {
		t.Fatalf("view %+v", v)
	}
	if len(e.audit.events) != 1 || e.audit.events[0].Action != "notification_template.updated" ||
		e.audit.events[0].ResourceID == nil {
		t.Fatalf("audit %+v", e.audit.events)
	}
	// 内容没变：不加版本、不写审计
	if v2, err := e.svc.SaveTemplate(e.ctx, TemplateEdit{Code: TemplatePasswordReset, Channel: ChannelEmail,
		Subject: v.Subject, Body: v.Body}); err != nil || v2.Version != 2 || len(e.audit.events) != 1 {
		t.Fatalf("no-op save %+v %v", v2, err)
	}
	// 白名单外变量与未登记模板
	if _, err := e.svc.SaveTemplate(e.ctx, TemplateEdit{Code: TemplatePasswordReset, Channel: ChannelEmail,
		Subject: "s", Body: "{{balance}}"}); !isCode(err, errs.ValidationFailed) {
		t.Fatalf("err %v", err)
	}
	if _, err := e.svc.SaveTemplate(e.ctx, TemplateEdit{Code: "nope.code", Channel: ChannelEmail,
		Subject: "s", Body: "b"}); !isCode(err, errs.NotFound) {
		t.Fatalf("err %v", err)
	}
	r, err := e.svc.ResetTemplate(e.ctx, TemplatePasswordReset, ChannelEmail)
	if err != nil || !r.IsDefault || r.Version != 3 {
		t.Fatalf("reset %+v %v", r, err)
	}
	list, err := e.svc.ListTemplates(e.ctx)
	if err != nil || len(list) != len(templateDefs) || list[0].Description == "" || list[0].PreviewSubject == list[0].Subject {
		t.Fatalf("list %+v %v", list, err)
	}
}

func TestPurgeDeliveries(t *testing.T) {
	e := newPG(t)
	e.send(t, TemplateEmailVerify)
	e.send(t, TemplatePasswordReset)
	if n, err := e.svc.PurgeDeliveries(e.ctx, time.Now().Add(-time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("purge old %d %v", n, err)
	}
	if n, err := e.svc.PurgeDeliveries(e.ctx, time.Now().Add(time.Hour), 1); err != nil || n != 1 {
		t.Fatalf("purge batch %d %v", n, err)
	}
	if rows := e.rows(t); len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
}
