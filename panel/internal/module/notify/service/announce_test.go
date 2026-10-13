package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func annValidInput() AnnouncementInput {
	return AnnouncementInput{
		Title:           "标题",
		Body:            "正文",
		Severity:        "info",
		ExpectedVersion: 0,
	}
}

func annFieldErr(err error, field string) string {
	e := errs.As(err)
	if e.Fields == nil {
		return ""
	}
	return e.Fields[field]
}

func TestAnnouncementStatus(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	st, pubAt, pubd := announcementStatus(false, nil, now)
	if st != model.AnnouncementDraft || pubAt != nil || pubd != nil {
		t.Fatalf("draft: st=%s pubAt=%v pubd=%v", st, pubAt, pubd)
	}

	st, pubAt, pubd = announcementStatus(false, &future, now)
	if st != model.AnnouncementDraft || pubd != nil {
		t.Fatalf("draft+future publish_at: st=%s pubd=%v", st, pubd)
	}

	st, pubAt, pubd = announcementStatus(true, &future, now)
	if st != model.AnnouncementScheduled || pubAt == nil || !pubAt.Equal(future) || pubd != nil {
		t.Fatalf("scheduled: st=%s pubAt=%v pubd=%v", st, pubAt, pubd)
	}

	st, pubAt, pubd = announcementStatus(true, nil, now)
	if st != model.AnnouncementPublished || pubAt == nil || !pubAt.Equal(now) || pubd == nil || !pubd.Equal(now) {
		t.Fatalf("publish now: st=%s pubAt=%v pubd=%v", st, pubAt, pubd)
	}

	st, pubAt, pubd = announcementStatus(true, &past, now)
	if st != model.AnnouncementPublished || pubAt == nil || !pubAt.Equal(past) || pubd == nil || !pubd.Equal(now) {
		t.Fatalf("publish past: st=%s pubAt=%v pubd=%v", st, pubAt, pubd)
	}

	st, pubAt, pubd = announcementStatus(true, &now, now)
	if st != model.AnnouncementPublished || pubd == nil || !pubd.Equal(now) {
		t.Fatalf("publish at now: st=%s pubd=%v", st, pubd)
	}
}

func TestCanSaveAs(t *testing.T) {
	cases := []struct {
		cur, next string
		ok        bool
	}{
		{model.AnnouncementDraft, model.AnnouncementDraft, true},
		{model.AnnouncementDraft, model.AnnouncementPublished, true},
		{model.AnnouncementScheduled, model.AnnouncementDraft, true},
		{model.AnnouncementPublished, model.AnnouncementPublished, true},
		{model.AnnouncementPublished, model.AnnouncementDraft, false},
		{model.AnnouncementPublished, model.AnnouncementScheduled, false},
		{model.AnnouncementWithdrawn, model.AnnouncementDraft, false},
		{model.AnnouncementWithdrawn, model.AnnouncementPublished, false},
	}
	for _, c := range cases {
		err := canSaveAs(c.cur, c.next)
		if c.ok && err != nil {
			t.Fatalf("%s -> %s: %v", c.cur, c.next, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s -> %s: expected error", c.cur, c.next)
		}
		if !c.ok && err != nil {
			if e := errs.As(err); e.Code != errs.Conflict {
				t.Fatalf("%s -> %s: code %s", c.cur, c.next, e.Code)
			}
		}
	}
}

func TestValidateAnnouncement_ok(t *testing.T) {
	if err := validateAnnouncement(annValidInput()); err != nil {
		t.Fatal(err)
	}
	in := annValidInput()
	in.Severity = ""
	if err := validateAnnouncement(in); err != nil {
		t.Fatal("empty severity allowed at validate layer")
	}
}

func TestValidateAnnouncement_titleBody(t *testing.T) {
	in := annValidInput()
	in.Title = "a"
	if msg := annFieldErr(validateAnnouncement(in), "title"); !strings.Contains(msg, "2") {
		t.Fatalf("title short: %q", msg)
	}
	in = annValidInput()
	in.Title = strings.Repeat("字", 161)
	if annFieldErr(validateAnnouncement(in), "title") == "" {
		t.Fatal("title long")
	}
	in = annValidInput()
	in.Body = "x"
	if annFieldErr(validateAnnouncement(in), "body") == "" {
		t.Fatal("body short")
	}
	in = annValidInput()
	in.Body = strings.Repeat("字", 20001)
	if annFieldErr(validateAnnouncement(in), "body") == "" {
		t.Fatal("body long")
	}
}

func TestValidateAnnouncement_nul(t *testing.T) {
	in := annValidInput()
	in.Title = "a\x00b"
	if annFieldErr(validateAnnouncement(in), "title") != "不能包含空字符" {
		t.Fatal("title nul")
	}
	in = annValidInput()
	in.Body = "a\x00b"
	if annFieldErr(validateAnnouncement(in), "body") != "不能包含空字符" {
		t.Fatal("body nul")
	}
}

func TestValidateAnnouncement_severity(t *testing.T) {
	in := annValidInput()
	in.Severity = "urgent"
	if annFieldErr(validateAnnouncement(in), "severity") == "" {
		t.Fatal("bad severity")
	}
	for _, s := range []string{"info", "notice", "warning", "critical"} {
		in := annValidInput()
		in.Severity = s
		if err := validateAnnouncement(in); err != nil {
			t.Fatalf("severity %s: %v", s, err)
		}
	}
}

func TestValidateAnnouncement_version(t *testing.T) {
	in := annValidInput()
	in.ExpectedVersion = 1
	if annFieldErr(validateAnnouncement(in), "expected_version") == "" {
		t.Fatal("new with version")
	}
	id := uuid.MustParse("018f4e88-b0e1-7c3e-8b3a-9c0d1e2f3a4b")
	in = annValidInput()
	in.ID = &id
	in.ExpectedVersion = 0
	if annFieldErr(validateAnnouncement(in), "expected_version") == "" {
		t.Fatal("edit without version")
	}
	in.ExpectedVersion = 2
	if err := validateAnnouncement(in); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAnnouncement_targetLimits(t *testing.T) {
	in := annValidInput()
	in.PlanIDs = make([]uuid.UUID, 201)
	if annFieldErr(validateAnnouncement(in), "target_plan_ids") == "" {
		t.Fatal("plan limit")
	}
	in = annValidInput()
	in.GroupIDs = make([]uuid.UUID, 201)
	if annFieldErr(validateAnnouncement(in), "target_user_group_ids") == "" {
		t.Fatal("group limit")
	}
}

func TestValidateAnnouncement_returnsInvalid(t *testing.T) {
	in := annValidInput()
	in.Title = ""
	err := validateAnnouncement(in)
	if err == nil {
		t.Fatal("expected error")
	}
	if errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("code %s", errs.As(err).Code)
	}
	if !errors.Is(err, errs.As(err)) {
		t.Fatal("as stable")
	}
}
