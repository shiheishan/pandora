package service

import (
	"strings"
	"testing"
)

func TestMarshalDataStripsNUL(t *testing.T) {
	raw, err := marshalData(map[string]any{"k\x00": []any{"a\x00b", map[string]any{"x": "\x00"}}, "n": 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\u0000`) {
		t.Fatalf("NUL 未去掉: %s", raw)
	}
	if string(raw) != `{"k":["ab",{"x":""}],"n":1}` {
		t.Fatalf("got %s", raw)
	}
	// 字面量的反斜杠加 u0000 不是 NUL，原样保留
	raw, err = marshalData(map[string]any{"s": `\u0000 字面量`})
	if err != nil || string(raw) != `{"s":"\\u0000 字面量"}` {
		t.Fatalf("got %s %v", raw, err)
	}
}

func TestCleanText(t *testing.T) {
	if got := cleanText("a\xffb\x00c"); got != "a�bc" {
		t.Fatalf("got %q", got)
	}
}
