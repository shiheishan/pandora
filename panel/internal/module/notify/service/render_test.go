package service

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestTemplateVars_ok(t *testing.T) {
	got, err := templateVars("你好 {{site}}，{{ code }} {{site}}")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"code", "site"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestTemplateVars_plainText(t *testing.T) {
	got, err := templateVars("纯文本")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %v want empty slice", got)
	}
}

func TestTemplateVars_syntaxErrors(t *testing.T) {
	cases := []string{
		"{{.site}}",
		"{{if site}}x{{end}}",
		"{{range 3}}x{{end}}",
		"{{site | printf \"%s\"}}",
		"{{printf \"%s\" site}}",
		"{{\"x\"}}",
		"{{$x := site}}",
		"{{define \"a\"}}x{{end}}",
		"{{site",
		"{{Site}}",
	}
	for _, text := range cases {
		_, err := templateVars(text)
		if !errors.Is(err, errTemplateSyntax) {
			t.Errorf("text %q: got %v want errTemplateSyntax", text, err)
		}
	}
}

func TestUnknownVariables(t *testing.T) {
	got, err := unknownVariables("{{site}} {{balance}} {{a}}", []string{"site"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "balance"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestRenderText_defaultSubject(t *testing.T) {
	def, ok := lookupDef(TemplateEmailVerify, ChannelEmail)
	if !ok {
		t.Fatal("lookupDef")
	}
	got, err := renderText(def.Subject, def.Allowed, map[string]string{"site": "S", "code": "123456"})
	if err != nil {
		t.Fatal(err)
	}
	want := "【S】注册验证码 123456"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderText_missingVarEmpty(t *testing.T) {
	got, err := renderText("{{site}}", []string{"site", "code"}, map[string]string{"site": "X"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "X" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderText_unknownVar(t *testing.T) {
	_, err := renderText("{{foo}}", []string{"site"}, map[string]string{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "未登记的变量") {
		t.Fatalf("got %v", err)
	}
}

func TestRenderText_literalNotReparsed(t *testing.T) {
	got, err := renderText("{{site}}", []string{"site"}, map[string]string{"site": "{{x}}"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "{{x}}" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderPreview_siteSample(t *testing.T) {
	sub, body := renderPreview("{{site}}", "", []string{"site"})
	if sub != "潘多拉面板" {
		t.Fatalf("subject %q", sub)
	}
	_ = body
}

func TestRenderPreview_noSamplePlaceholder(t *testing.T) {
	_, body := renderPreview("", "{{foo}}", []string{"foo"})
	if body != "{{foo}}" {
		t.Fatalf("body %q", body)
	}
}

func TestRenderPreview_syntaxBodyUnchanged(t *testing.T) {
	bad := "{{if x}}y{{end}}"
	_, body := renderPreview("ok", bad, []string{"site"})
	if body != bad {
		t.Fatalf("body %q want %q", body, bad)
	}
}

func TestValidateTemplateContent_ok(t *testing.T) {
	sub, body, err := validateTemplateContent("  hi {{site}}  ", "  body {{site}}  ", []string{"site"})
	if err != nil {
		t.Fatal(err)
	}
	if sub != "hi {{site}}" || body != "body {{site}}" {
		t.Fatalf("sub=%q body=%q", sub, body)
	}
}

func assertValidationField(t *testing.T, err error, field, msg string) {
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("not *errs.Error: %v", err)
	}
	if e.Fields[field] != msg {
		t.Fatalf("field %q: got %q want %q", field, e.Fields[field], msg)
	}
}

func TestValidateTemplateContent_emptySubject(t *testing.T) {
	_, _, err := validateTemplateContent("   ", "正文 {{site}}", []string{"site"})
	assertValidationField(t, err, "subject", "主题必填，且不超过 200 字")
}

func TestValidateTemplateContent_bodyTooLong(t *testing.T) {
	long := strings.Repeat("字", 20001)
	_, _, err := validateTemplateContent("主题 {{site}}", long, []string{"site"})
	assertValidationField(t, err, "body", "正文必填，且不超过 20000 字")
}

func TestValidateTemplateContent_nul(t *testing.T) {
	_, _, err := validateTemplateContent("a\x00b", "正文", []string{"site"})
	assertValidationField(t, err, "subject", "不能包含空字符")
}

func TestValidateTemplateContent_subjectNewline(t *testing.T) {
	_, _, err := validateTemplateContent("a\nb", "正文 {{site}}", []string{"site"})
	assertValidationField(t, err, "subject", "主题不能换行")
}

func TestValidateTemplateContent_syntax(t *testing.T) {
	_, _, err := validateTemplateContent("{{.x}}", "正文 {{site}}", []string{"site"})
	assertValidationField(t, err, "subject", errTemplateSyntax.Error())
}

func TestValidateTemplateContent_unknownVar(t *testing.T) {
	_, _, err := validateTemplateContent("主题", "{{nope}}", []string{"site"})
	want := "用到了这个模板不提供的变量：nope。可用变量：site"
	assertValidationField(t, err, "body", want)
}

func TestHeaderSafe(t *testing.T) {
	got := headerSafe("a\r\nb\n")
	want := "a  b"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestValidateTemplateContent_subjectMaxRunes(t *testing.T) {
	sub := strings.Repeat("字", 201)
	_, _, err := validateTemplateContent(sub, "正文", []string{})
	assertValidationField(t, err, "subject", "主题必填，且不超过 200 字")
	if utf8.RuneCountInString(sub) != 201 {
		t.Fatal("rune count")
	}
}
