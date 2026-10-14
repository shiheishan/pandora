package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/module/notify/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// annRTStore 只实现公告写路径用到的 store 方法。
type annRTStore struct {
	store
	rows map[uuid.UUID]repo.AnnouncementRow
	due  []repo.PublishedAnnouncement
}

func (s *annRTStore) InsertAnnouncement(_ context.Context, a *repo.AnnouncementRow) error {
	a.ID = uuid.New()
	a.Version = 1
	s.rows[a.ID] = *a
	return nil
}

func (s *annRTStore) LockAnnouncement(_ context.Context, id uuid.UUID) (repo.AnnouncementRow, bool, error) {
	row, ok := s.rows[id]
	return row, ok, nil
}

func (s *annRTStore) UpdateAnnouncement(_ context.Context, a repo.AnnouncementRow, expectedVersion int) (int, bool, error) {
	cur, ok := s.rows[a.ID]
	if !ok || cur.Version != expectedVersion {
		return 0, false, nil
	}
	a.Version = expectedVersion + 1
	s.rows[a.ID] = a
	return a.Version, true, nil
}

func (s *annRTStore) WithdrawAnnouncement(_ context.Context, id, _ uuid.UUID, expectedVersion int) (int, bool, error) {
	cur, ok := s.rows[id]
	if !ok || cur.Version != expectedVersion {
		return 0, false, nil
	}
	cur.Status = model.AnnouncementWithdrawn
	cur.Version = expectedVersion + 1
	s.rows[id] = cur
	return cur.Version, true, nil
}

func (s *annRTStore) PublishDueAnnouncements(context.Context, time.Time) ([]repo.PublishedAnnouncement, error) {
	return s.due, nil
}

func newAnnRT(t *testing.T) (*Service, *annRTStore, *rtSpy, context.Context) {
	t.Helper()
	st := &annRTStore{rows: map[uuid.UUID]repo.AnnouncementRow{}}
	svc, err := newService(st, passTx{}, testKeys(t), &fakeSettings{mail: configuredMail()}, &fakeAudit{}, &fakeQueue{}, &fakeSender{})
	if err != nil {
		t.Fatal(err)
	}
	svc.UseDirectory(newFakeDir())
	svc.UsePlans(&fakePlans{})
	subs := newFakeSubs()
	svc.UseSubscriptions(subs)
	svc.UseOrders(subs)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	svc.UseRealtime(pub)
	admin := authn.With(context.Background(), authn.Principal{UserID: uuid.New(), Audience: authn.AudienceAdmin})
	return svc, st, spy, admin
}

func expectAnnRT(t *testing.T, spy *rtSpy, id uuid.UUID, op string) {
	t.Helper()
	want := map[string]any{"table": "announcements", "op": op, "id": id.String()}
	spy.expect(t, realtime.ChannelPublic(), realtime.TopicAnnouncements, want)
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicAnnouncements, want)
}

func TestRealtimeAnnouncementCreate(t *testing.T) {
	svc, _, spy, admin := newAnnRT(t)
	saved, err := svc.SaveAnnouncement(admin, AnnouncementInput{Title: "维护通知", Body: "正文内容", Severity: "info"})
	if err != nil {
		t.Fatal(err)
	}
	expectAnnRT(t, spy, saved.ID, realtime.OpInsert)
}

func TestRealtimeAnnouncementEdit(t *testing.T) {
	svc, _, spy, admin := newAnnRT(t)
	saved, err := svc.SaveAnnouncement(admin, AnnouncementInput{Title: "维护通知", Body: "正文内容", Severity: "info"})
	if err != nil {
		t.Fatal(err)
	}
	expectAnnRT(t, spy, saved.ID, realtime.OpInsert)

	id := saved.ID
	edit := AnnouncementInput{
		ID: &id, ExpectedVersion: saved.Version, Publish: true,
		Title: "维护通知", Body: "正文内容", Severity: "info",
	}
	if _, err := svc.SaveAnnouncement(admin, edit); err != nil {
		t.Fatal(err)
	}
	expectAnnRT(t, spy, saved.ID, realtime.OpUpdate)
}

func TestRealtimeAnnouncementWithdraw(t *testing.T) {
	svc, _, spy, admin := newAnnRT(t)
	saved, err := svc.SaveAnnouncement(admin, AnnouncementInput{Title: "维护通知", Body: "正文内容", Severity: "info"})
	if err != nil {
		t.Fatal(err)
	}
	expectAnnRT(t, spy, saved.ID, realtime.OpInsert)

	ver, err := svc.WithdrawAnnouncement(admin, saved.ID, saved.Version)
	if err != nil || ver != saved.Version+1 {
		t.Fatalf("withdraw ver=%d err=%v", ver, err)
	}
	expectAnnRT(t, spy, saved.ID, realtime.OpUpdate)
}

func TestRealtimeAnnouncementPublishDue(t *testing.T) {
	svc, st, spy, _ := newAnnRT(t)
	x := uuid.New()
	st.due = []repo.PublishedAnnouncement{{ID: x, Version: 2}}
	if n, err := svc.PublishDueAnnouncements(context.Background()); err != nil || n != 1 {
		t.Fatalf("publish due n=%d err=%v", n, err)
	}
	expectAnnRT(t, spy, x, realtime.OpUpdate)
}

func TestRealtimeAnnouncementConflictDoesNotPublish(t *testing.T) {
	svc, _, spy, admin := newAnnRT(t)
	saved, err := svc.SaveAnnouncement(admin, AnnouncementInput{Title: "维护通知", Body: "正文内容", Severity: "info"})
	if err != nil {
		t.Fatal(err)
	}
	expectAnnRT(t, spy, saved.ID, realtime.OpInsert)

	id := saved.ID
	bad := AnnouncementInput{
		ID: &id, ExpectedVersion: saved.Version + 99,
		Title: "维护通知", Body: "正文内容", Severity: "info",
	}
	if _, err := svc.SaveAnnouncement(admin, bad); !isCode(err, errs.Conflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	spy.expectNone(t, 200*time.Millisecond)
}
