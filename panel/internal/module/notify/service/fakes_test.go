package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"sync"
	"testing"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/hibiken/asynq"
)

// testKeys 是测试用主密钥。
func testKeys(t *testing.T) *crypto.Keys {
	t.Helper()
	k, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// fakeSettings 是内存里的 settings。
type fakeSettings struct {
	mail     settingssvc.Mail
	reg      settingssvc.Registration
	site     settingssvc.Site
	emailOff bool

	setMail    *settingssvc.MailUpdate
	setMailErr error
	setReg     *settingssvc.Registration
	setRegErr  error
	calls      []string
}

func (f *fakeSettings) Mail(context.Context) (settingssvc.Mail, error) { return f.mail, nil }

func (f *fakeSettings) SetMail(_ context.Context, v settingssvc.MailUpdate) error {
	f.calls = append(f.calls, "SetMail")
	f.setMail = &v
	return f.setMailErr
}

func (f *fakeSettings) Registration(context.Context) (settingssvc.Registration, error) {
	return f.reg, nil
}

func (f *fakeSettings) SetRegistration(_ context.Context, v settingssvc.Registration) error {
	f.calls = append(f.calls, "SetRegistration")
	f.setReg = &v
	return f.setRegErr
}

func (f *fakeSettings) Site(context.Context) (settingssvc.Site, error) { return f.site, nil }

func (f *fakeSettings) SwitchEnabled(_ context.Context, code string) (bool, error) {
	if code == settingssvc.SwitchNotifyEmail {
		return !f.emailOff, nil
	}
	return true, nil
}

// configuredMail 是一份齐全的 SMTP 配置。
func configuredMail() settingssvc.Mail {
	return settingssvc.Mail{
		Host: "smtp.example.com", Port: 465, Encryption: settingssvc.MailEncryptionSSL,
		FromAddress: "noreply@example.com",
	}
}

// fakeAudit 记下写过的审计。
type fakeAudit struct {
	events []auditsvc.Event
	err    error
}

func (f *fakeAudit) Write(_ context.Context, e auditsvc.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

// fakeSender 记下发出的邮件，按顺序返回 errs 里的错误（用完后返回 nil）。
type fakeSender struct {
	mu   sync.Mutex
	sent []Message
	cfgs []SMTPConfig
	errs []error
}

func (f *fakeSender) Send(_ context.Context, cfg SMTPConfig, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	f.cfgs = append(f.cfgs, cfg)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return err
	}
	return nil
}

// fakeQueue 记下入队的任务。
type fakeQueue struct{ tasks []*asynq.Task }

func (f *fakeQueue) EnqueueContext(_ context.Context, t *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	f.tasks = append(f.tasks, t)
	return &asynq.TaskInfo{}, nil
}

// passTx 直接执行 fn，给不碰数据库的单测用。
type passTx struct{}

func (passTx) Do(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }
