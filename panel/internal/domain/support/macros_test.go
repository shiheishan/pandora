package support

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 长度按字符而不是字节计：20 个汉字是 60 字节，按字节算会把合法标题拒掉。
func TestNormalizeMacroInput(t *testing.T) {
	in, err := normalizeMacroInput(MacroInput{Title: "  " + strings.Repeat("退", 20) + " ", Body: " 请先重启客户端 "})
	if err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if in.Title != strings.Repeat("退", 20) || in.Body != "请先重启客户端" {
		t.Fatalf("not trimmed: %+v", in)
	}
	for _, tc := range []struct {
		in    MacroInput
		field string
	}{
		{MacroInput{Title: "   ", Body: "x"}, "title"},
		{MacroInput{Title: strings.Repeat("退", 21), Body: "x"}, "title"},
		{MacroInput{Title: "t", Body: "  "}, "body"},
		{MacroInput{Title: "t", Body: strings.Repeat("a", 5001)}, "body"},
	} {
		_, err := normalizeMacroInput(tc.in)
		var httpErr *httpx.Error
		if !errors.As(err, &httpErr) || httpErr.Code != httpx.CodeValidationFailed || httpErr.Fields[tc.field] == "" {
			t.Errorf("%+v: want 422 fields.%s, got %v", tc.in, tc.field, err)
		}
	}
}
