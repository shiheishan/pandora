package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// BulkRecipients 让 fakeDir 继续满足 Directory；需要收件人的用例用 bulkDir。
func (f *fakeDir) BulkRecipients(context.Context, identitysvc.BulkFilter, int) ([]identitysvc.BulkRecipient, int64, error) {
	return nil, 0, nil
}

// bulkDir 在 fakeDir 之上给出固定的群发收件人，并记下收到的筛选与上限。
type bulkDir struct {
	*fakeDir
	recips []identitysvc.BulkRecipient
	total  int64
	err    error
	got    identitysvc.BulkFilter
	gotMax int
}

func (b *bulkDir) BulkRecipients(_ context.Context, f identitysvc.BulkFilter, max int) ([]identitysvc.BulkRecipient, int64, error) {
	b.got, b.gotMax = f, max
	return b.recips, b.total, b.err
}

func TestSendBulkMailValidation(t *testing.T) {
	svc, _, _ := newUnit(t, &fakeSettings{})
	cases := []BulkMailInput{
		{Subject: "  ", Body: ""},
		{Subject: strings.Repeat("主", 201), Body: strings.Repeat("正", 20001)},
		{Subject: "a\x00b", Body: "c\x00d"},
	}
	for i, in := range cases {
		_, err := svc.SendBulkMail(context.Background(), in)
		f := fieldsOf(t, err)
		if f["subject"] == "" || f["body"] == "" {
			t.Fatalf("case %d fields %v", i, f)
		}
	}

	for _, total := range []int64{0, 20001} {
		bd := &bulkDir{fakeDir: newFakeDir(), total: total}
		svc.UseDirectory(bd)
		_, err := svc.SendBulkMail(context.Background(), BulkMailInput{Subject: "s", Body: "b"})
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != errs.ValidationFailed {
			t.Fatalf("total %d err %v", total, err)
		}
	}
}

func TestSendBulkMailPG(t *testing.T) {
	e := newPG(t)
	dir, _, _ := e.usePorts()
	u1 := e.insertTestUser(t, dir)
	u2 := e.insertTestUser(t, dir)
	u3 := e.insertTestUser(t, dir)
	var pref struct{ UserID uuid.UUID }
	if err := e.scan(e.ctx, &pref, `INSERT INTO notification_preferences (user_id, category, channel, enabled)
		VALUES (?, 'marketing', 'email', false) RETURNING user_id`, u3); err != nil || pref.UserID != u3 {
		t.Fatalf("偏好 %v", err)
	}
	end := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	bd := &bulkDir{fakeDir: dir, total: 3, recips: []identitysvc.BulkRecipient{
		{UserID: u1, Email: "b1@example.test", PlanName: "月付", PeriodEnd: &end},
		{UserID: u2, Email: "b2@example.test"},
		{UserID: u3, Email: "b3@example.test"},
	}}
	e.svc.UseDirectory(bd)

	in := BulkMailInput{
		Filter:  identitysvc.BulkFilter{Status: "active"},
		Subject: "续费提醒",
		Body:    "$email 的 $plan 在 $expire 到期",
	}
	res, err := e.svc.SendBulkMail(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Queued != 2 || res.Skipped != 1 {
		t.Fatalf("结果 %+v", res)
	}
	if bd.gotMax != 20000 || bd.got.Status != "active" {
		t.Fatalf("筛选 %+v max %d", bd.got, bd.gotMax)
	}

	rows := e.rows(t)
	if len(rows) != 2 {
		t.Fatalf("投递 %d 行", len(rows))
	}
	for _, r := range rows {
		if r.TemplateCode != TemplateBroadcast || r.Status != "queued" || !r.HasPayload {
			t.Fatalf("投递 %+v", r)
		}
	}

	ds, err := e.repo.ClaimDeliveries(e.ctx, ChannelEmail, 10, time.Minute)
	if err != nil || len(ds) != 2 {
		t.Fatalf("claim %d %v", len(ds), err)
	}
	bodies := map[string]string{}
	for _, d := range ds {
		if d.UserID == nil || *d.UserID == u3 {
			t.Fatalf("不该给关掉营销邮件的用户排投递：%+v", d.UserID)
		}
		raw, err := e.svc.sealer.Open(d.PayloadEnc, deliveryAAD(d.ID))
		if err != nil {
			t.Fatal(err)
		}
		var p deliveryPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if p.Vars["subject"] != "续费提醒" {
			t.Fatalf("主题 %q", p.Vars["subject"])
		}
		bodies[p.To] = p.Vars["body"]
	}
	if got := bodies["b1@example.test"]; got != "b1@example.test 的 月付 在 2026-11-01 到期" {
		t.Fatalf("正文替换 %q", got)
	}
	if got := bodies["b2@example.test"]; got != "b2@example.test 的  在  到期" {
		t.Fatalf("无订阅的正文 %q", got)
	}

	var sent int
	for _, ev := range e.audit.events {
		if ev.Action != "user.bulk_mail_sent" {
			continue
		}
		sent++
		if ev.Data["matched"] != int64(3) || ev.Data["queued"] != 2 || ev.Data["skipped_by_preference"] != 1 {
			t.Fatalf("审计 %+v", ev.Data)
		}
	}
	if sent != 1 {
		t.Fatalf("审计 user.bulk_mail_sent %d 条", sent)
	}
}
