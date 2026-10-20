package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

type homeCounter struct{ n int }

func (h *homeCounter) HomeSettingsChanged() { h.n++ }

func TestHomeDefaults(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newService(st, fakeTx{}, nil, nil, &auditLog{})
	home, err := s.Home(context.Background())
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	if !home.Enabled || home.Intro != "" || home.FAQ == nil || len(home.FAQ) != 0 {
		t.Fatalf("defaults: %+v", home)
	}
}

func TestSetHomeNormalizesAndAudits(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	ln := &homeCounter{}
	s.UseHomeChangeListener(ln)
	ctx := context.Background()

	intro := "  第一行\r\n第二行  "
	faq := []HomeFAQ{{Q: "  能退款吗？ ", A: " 不能\r\n请先试用 "}}
	if err := s.SetHome(ctx, HomeUpdate{Intro: &intro, FAQ: &faq}); err != nil {
		t.Fatalf("set: %v", err)
	}
	home, err := s.Home(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if home.Intro != "第一行\n第二行" {
		t.Fatalf("intro %q", home.Intro)
	}
	wantFAQ := HomeFAQ{Q: "能退款吗？", A: "不能\n请先试用"}
	if len(home.FAQ) != 1 || home.FAQ[0] != wantFAQ {
		t.Fatalf("faq %+v want %+v", home.FAQ, wantFAQ)
	}
	if len(al.entries) != 1 || al.entries[0].action != auditHomeChanged {
		t.Fatalf("audit %+v", al.entries)
	}
	if ln.n != 1 {
		t.Fatalf("listener %d", ln.n)
	}
}

func TestSetHomeNoopDoesNotWriteOrNotify(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	ln := &homeCounter{}
	s.UseHomeChangeListener(ln)
	ctx := context.Background()

	intro := "第一行\n第二行"
	faq := []HomeFAQ{{Q: "能退款吗？", A: "不能\n请先试用"}}
	if err := s.SetHome(ctx, HomeUpdate{Intro: &intro, FAQ: &faq}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.SetHome(ctx, HomeUpdate{Intro: &intro, FAQ: &faq}); err != nil {
		t.Fatalf("same: %v", err)
	}
	if len(al.entries) != 1 || ln.n != 1 {
		t.Fatalf("after noop audit=%d listener=%d", len(al.entries), ln.n)
	}
	disabled := false
	if err := s.SetHome(ctx, HomeUpdate{Enabled: &disabled}); err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if len(al.entries) != 2 || ln.n != 2 {
		t.Fatalf("after enabled audit=%d listener=%d", len(al.entries), ln.n)
	}
}

func TestSetHomePartialKeepsOthers(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newService(st, fakeTx{}, nil, nil, &auditLog{})
	ctx := context.Background()

	intro := "虚构站介绍"
	faq := []HomeFAQ{{Q: "能试用吗？", A: "可以"}}
	if err := s.SetHome(ctx, HomeUpdate{Intro: &intro, FAQ: &faq}); err != nil {
		t.Fatalf("set: %v", err)
	}
	enabled := false
	if err := s.SetHome(ctx, HomeUpdate{Enabled: &enabled}); err != nil {
		t.Fatalf("partial: %v", err)
	}
	home, err := s.Home(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if home.Enabled || home.Intro != intro || len(home.FAQ) != 1 || home.FAQ[0] != faq[0] {
		t.Fatalf("partial keep: %+v", home)
	}
}

func TestSetHomeValidation(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	ln := &homeCounter{}
	s.UseHomeChangeListener(ln)
	ctx := context.Background()

	longIntro := strings.Repeat("字", HomeIntroMax+1)
	err := s.SetHome(ctx, HomeUpdate{Intro: &longIntro})
	assertHomeFieldMsg(t, err, "intro", "介绍最多 500 个字")

	badIntro := "a\x07b"
	err = s.SetHome(ctx, HomeUpdate{Intro: &badIntro})
	assertHomeFieldMsg(t, err, "intro", "介绍不能包含控制字符")

	manyFAQ := make([]HomeFAQ, HomeFAQMax+1)
	for i := range manyFAQ {
		manyFAQ[i] = HomeFAQ{Q: "问", A: "答"}
	}
	err = s.SetHome(ctx, HomeUpdate{FAQ: &manyFAQ})
	assertHomeFieldMsg(t, err, "faq", "常见问题最多 20 条")

	emptyQ := []HomeFAQ{{Q: "  ", A: "有答"}}
	err = s.SetHome(ctx, HomeUpdate{FAQ: &emptyQ})
	assertHomeFieldMsg(t, err, "faq.0.q", "问题必填")

	emptyA := []HomeFAQ{{Q: "第一问", A: "有答"}, {Q: "第二问", A: "  "}}
	err = s.SetHome(ctx, HomeUpdate{FAQ: &emptyA})
	assertHomeFieldMsg(t, err, "faq.1.a", "回答必填")

	longQ := strings.Repeat("问", HomeFAQQuestionMax+1)
	err = s.SetHome(ctx, HomeUpdate{FAQ: &[]HomeFAQ{{Q: longQ, A: "答"}}})
	assertHomeFieldMsg(t, err, "faq.0.q", "问题最多 100 个字")

	qNewline := []HomeFAQ{{Q: "a\nb", A: "答"}}
	err = s.SetHome(ctx, HomeUpdate{FAQ: &qNewline})
	assertHomeFieldMsg(t, err, "faq.0.q", "问题不能包含换行或控制字符")

	longA := strings.Repeat("答", HomeFAQAnswerMax+1)
	err = s.SetHome(ctx, HomeUpdate{FAQ: &[]HomeFAQ{{Q: "问", A: longA}}})
	assertHomeFieldMsg(t, err, "faq.0.a", "回答最多 500 个字")

	badA := []HomeFAQ{{Q: "问", A: "a\x00b"}}
	err = s.SetHome(ctx, HomeUpdate{FAQ: &badA})
	assertHomeFieldMsg(t, err, "faq.0.a", "回答不能包含控制字符")

	if len(al.entries) != 0 || ln.n != 0 {
		t.Fatalf("validation should not audit/notify: audit=%d listener=%d", len(al.entries), ln.n)
	}
}

func assertHomeFieldMsg(t *testing.T, err error, field, want string) {
	t.Helper()
	e := errs.As(err)
	if e.Code != errs.ValidationFailed {
		t.Fatalf("code=%s field=%s", e.Code, field)
	}
	if e.Fields == nil || e.Fields[field] != want {
		t.Fatalf("field %q msg=%q want %q all=%v", field, e.Fields[field], want, e.Fields)
	}
}

func TestMissingPortsListsHomeListener(t *testing.T) {
	s := newService(&kvStore{kv: map[string]json.RawMessage{}}, fakeTx{}, nil, nil, &auditLog{})
	ports := s.MissingPorts()
	if !slices.Contains(ports, "HomeChangeListener") {
		t.Fatalf("MissingPorts %v", ports)
	}
	s.UseHomeChangeListener(&homeCounter{})
	ports = s.MissingPorts()
	if slices.Contains(ports, "HomeChangeListener") {
		t.Fatalf("after inject %v", ports)
	}
}
