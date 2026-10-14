package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
)

func TestAdminTelegramBindingPG(t *testing.T) {
	e := newPG(t)
	uid := e.insertTestUser(t, nil)

	ref, err := e.svc.AdminTelegramBinding(e.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if ref != nil {
		t.Fatalf("未绑定应返回 nil，得到 %+v", ref)
	}

	wantUsername := "alice_notify"
	if err := e.repo.InsertBinding(e.ctx, &model.TelegramBinding{
		UserID: uid, ChatID: time.Now().UnixNano(), Username: wantUsername, // chat_id 全库唯一，取随机值免撞
	}); err != nil {
		t.Fatal(err)
	}

	ref, err = e.svc.AdminTelegramBinding(e.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if ref == nil {
		t.Fatal("已绑定不应为 nil")
	}
	if ref.Username != wantUsername {
		t.Fatalf("username %q，期望 %q", ref.Username, wantUsername)
	}
	if ref.BoundAt.IsZero() {
		t.Fatal("BoundAt 应为非零")
	}
}
