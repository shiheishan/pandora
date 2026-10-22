package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

// integrityDeliveries 读巡检告警模板的全部投递（新的在后）；库里可能有别的包提交的行，用例只比前后差。
func integrityDeliveries(t *testing.T, e *pgEnv) []model.Delivery {
	t.Helper()
	var out []model.Delivery
	if err := e.scan(e.ctx, &out, `SELECT * FROM notification_deliveries WHERE template_code = ? ORDER BY id`,
		TemplateAdminIntegrityAlert); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNotifyAdminsIntegrity(t *testing.T) {
	e := newPG(t)
	adminChatID := int64(-1001234500077)
	e.st.tg = settingssvc.Telegram{
		Enabled:     true,
		BotUsername: "admin_bot",
		BotToken:    "123:abc",
		HasToken:    true,
		AdminChatID: &adminChatID,
	}
	code := "test_check_" + uuid.NewString()[:8]
	since := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	base := len(integrityDeliveries(t, e))

	ok, err := e.svc.NotifyAdminsIntegrity(e.ctx, IntegrityAlert{Code: code, Name: "测试检查", Count: 2, Since: since})
	if err != nil || !ok {
		t.Fatalf("首次告警 ok=%v err=%v", ok, err)
	}
	ds := integrityDeliveries(t, e)
	if len(ds) != base+1 {
		t.Fatalf("应新增 1 条告警投递，实际 %d → %d", base, len(ds))
	}
	d := ds[len(ds)-1]
	if d.Channel != ChannelTelegram || d.Status != model.StatusQueued || d.UserID != nil {
		t.Fatalf("投递形状不对 %+v", d)
	}
	p := noticeOpenPayload(t, e, d)
	if p.To != "-1001234500077" || p.Vars["check"] != "测试检查" || p.Vars["code"] != code || p.Vars["count"] != "2" || len(p.Vars) != 3 {
		t.Fatalf("payload %+v", p)
	}
	// 去重键不关某个用户：user_id 为空
	var nullUser int64
	if err := e.scan(e.ctx, &nullUser, `SELECT count(*) FROM notification_events WHERE dedupe_key = ? AND user_id IS NULL`,
		"integrity:"+code+":"+strconv.FormatInt(since.Unix(), 10)+":2"); err != nil || nullUser != 1 {
		t.Fatalf("去重键 %d %v", nullUser, err)
	}

	// 同一段违例、同一个数：撞去重键，不再写，但仍回 true（已排过）
	ok, err = e.svc.NotifyAdminsIntegrity(e.ctx, IntegrityAlert{Code: code, Name: "测试检查", Count: 2, Since: since})
	if err != nil || !ok {
		t.Fatalf("重复告警 ok=%v err=%v", ok, err)
	}
	if n := len(integrityDeliveries(t, e)); n != base+1 {
		t.Fatalf("重复告警不该再写，实际 %d 条", n)
	}

	// 数变了是新的一条
	if ok, err := e.svc.NotifyAdminsIntegrity(e.ctx, IntegrityAlert{Code: code, Name: "测试检查", Count: 3, Since: since}); err != nil || !ok {
		t.Fatalf("变多告警 ok=%v err=%v", ok, err)
	}
	if n := len(integrityDeliveries(t, e)); n != base+2 {
		t.Fatalf("变多应再写一条，实际 %d 条", n)
	}
}

func TestNotifyAdminsIntegrityNoGroup(t *testing.T) {
	e := newPG(t)
	e.st.tg = settingssvc.Telegram{
		Enabled:     true,
		BotUsername: "admin_bot",
		BotToken:    "123:abc",
		HasToken:    true,
	}
	code := "test_check_" + uuid.NewString()[:8]
	ok, err := e.svc.NotifyAdminsIntegrity(e.ctx, IntegrityAlert{Code: code, Name: "测试检查", Count: 1, Since: time.Now()})
	if err != nil || ok {
		t.Fatalf("没配群组应什么都不发 ok=%v err=%v", ok, err)
	}
	var n int64
	if err := e.scan(e.ctx, &n, `SELECT count(*) FROM notification_events WHERE dedupe_key LIKE ?`, "integrity:"+code+":%"); err != nil || n != 0 {
		t.Fatalf("没配群组不该写去重键 %d %v", n, err)
	}
}
