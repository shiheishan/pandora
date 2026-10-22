package service

import (
	"context"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

// 后台改了主域名：webhook 已注册时用现有 secret 重新注册到新地址、不丢排队的更新，并同步命令菜单。
func TestSiteURLChangedReregistersWebhook(t *testing.T) {
	ready := settingssvc.Telegram{Enabled: true, BotUsername: "mybot", BotToken: "tok-1", HasToken: true, WebhookSecret: "whsec"}

	t.Run("已注册就重注册到新地址", func(t *testing.T) {
		st := &fakeSettings{tg: ready, tgx: settingssvc.TelegramExtras{JoinReview: true}, siteURL: "https://new.example.test/"}
		svc, _, _ := newUnit(t, st)
		tg := &fakeTG{}
		svc.tg = tg
		svc.SiteURLChanged(context.Background())
		if len(tg.webhooks) != 1 {
			t.Fatalf("webhooks %d", len(tg.webhooks))
		}
		w := tg.webhooks[0]
		if w.URL != "https://new.example.test/v1/webhooks/telegram" || w.Secret != "whsec" || w.DropPending {
			t.Fatalf("webhook %+v", w)
		}
		if len(w.AllowedUpdates) != 2 || w.AllowedUpdates[1] != "chat_join_request" {
			t.Fatalf("allowed_updates %v", w.AllowedUpdates)
		}
		if len(tg.commands) != 1 {
			t.Fatalf("commands synced %d times", len(tg.commands))
		}
		if st.setTg != nil {
			t.Fatal("重注册不换 secret，不写 Telegram 设置")
		}
	})

	t.Run("没启用不注册", func(t *testing.T) {
		off := ready
		off.Enabled = false
		svc, _, _ := newUnit(t, &fakeSettings{tg: off, siteURL: "https://new.example.test/"})
		tg := &fakeTG{}
		svc.tg = tg
		svc.SiteURLChanged(context.Background())
		if len(tg.webhooks) != 0 {
			t.Fatal("disabled bot must not be registered")
		}
	})

	t.Run("没有 secret 不注册", func(t *testing.T) {
		noSecret := ready
		noSecret.WebhookSecret = ""
		svc, _, _ := newUnit(t, &fakeSettings{tg: noSecret, siteURL: "https://new.example.test/"})
		tg := &fakeTG{}
		svc.tg = tg
		svc.SiteURLChanged(context.Background())
		if len(tg.webhooks) != 0 {
			t.Fatal("never-registered bot must not be registered")
		}
	})

	t.Run("新地址不是 https 不注册", func(t *testing.T) {
		svc, _, _ := newUnit(t, &fakeSettings{tg: ready, siteURL: "http://new.example.test/"})
		tg := &fakeTG{}
		svc.tg = tg
		svc.SiteURLChanged(context.Background())
		if len(tg.webhooks) != 0 {
			t.Fatal("http must not be registered")
		}
	})

	t.Run("Telegram 报错只记日志", func(t *testing.T) {
		svc, _, _ := newUnit(t, &fakeSettings{tg: ready, siteURL: "https://new.example.test/"})
		tg := &fakeTG{webhookErr: &TelegramError{Status: 400, Detail: "bad hook"}}
		svc.tg = tg
		svc.SiteURLChanged(context.Background()) // 不 panic、不返回错误
		if len(tg.commands) != 0 {
			t.Fatal("webhook 失败时不同步命令菜单")
		}
	})
}
