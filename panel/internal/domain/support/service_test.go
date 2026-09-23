package support

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestNormalizeCreateInputUsesBodyOnlyDefaults(t *testing.T) {
	in := CreateInput{Body: "\n  无法连接节点，客户端一直超时。\n更多上下文"}

	normalizeCreateInput(&in)

	if got, want := in.Subject, "无法连接节点，客户端一直超时。"; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
	if got, want := in.Category, defaultTicketCategory; got != want {
		t.Fatalf("category = %q, want %q", got, want)
	}
}

func TestNormalizeCreateInputPreservesExplicitLegacyFields(t *testing.T) {
	in := CreateInput{
		Subject:  "  原有客户端标题  ",
		Category: " billing ",
		Body:     "  这是满足最小长度的工单正文。  ",
	}

	normalizeCreateInput(&in)

	if got, want := in.Subject, "原有客户端标题"; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
	if got, want := in.Category, "billing"; got != want {
		t.Fatalf("category = %q, want %q", got, want)
	}
}

func TestSubjectFromBodyIsUnicodeSafeAndMeetsMinimumLength(t *testing.T) {
	short := subjectFromBody("猫")
	if got, want := short, "用户咨询：猫"; got != want {
		t.Fatalf("short subject = %q, want %q", got, want)
	}

	long := subjectFromBody(strings.Repeat("😀", 121))
	if got, want := len([]rune(long)), 120; got != want {
		t.Fatalf("long subject rune length = %d, want %d", got, want)
	}
	if !strings.HasSuffix(long, "😀") {
		t.Fatalf("long subject ends with a partial Unicode character: %q", long)
	}
}

func TestSubjectFromBodySkipsBlankLines(t *testing.T) {
	if got, want := subjectFromBody(" \n\t\n第一行标题\n第二行内容"), "第一行标题"; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
}

func TestCreateRejectsEmptyAndShortBodyBeforeDatabaseAccess(t *testing.T) {
	for _, body := range []string{"", "太短"} {
		_, err := (&Service{}).Create(t.Context(), "tenant-test", CreateInput{
			UserID: "user-test",
			Body:   body,
		})
		he, ok := err.(*httpx.Error)
		if !ok || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("body %q error = %#v, want validation_failed", body, err)
		}
		if _, ok := he.Fields["body"]; !ok {
			t.Fatalf("body %q fields = %#v, want body validation", body, he.Fields)
		}
	}
}
