package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeaderValidate(t *testing.T) {
	if err := (Header{}).Validate(); err != nil {
		t.Fatalf("全空应通过: %v", err)
	}

	longCT := strings.Repeat("x", 256)
	if err := (Header{ContentType: longCT}).Validate(); err == nil {
		t.Fatal("Content-Type 256 字节应失败")
	}

	if err := (Header{Location: "/ok"}).Validate(); err != nil {
		t.Fatalf("Location /ok 应通过: %v", err)
	}
	for _, loc := range []string{"https://x", "//x", "/a\\b"} {
		if err := (Header{Location: loc}).Validate(); err == nil {
			t.Fatalf("Location %q 应失败", loc)
		}
	}

	for _, bad := range []string{"a\r", "a\n", "\r", "\n"} {
		if err := (Header{ETag: bad}).Validate(); err == nil {
			t.Fatalf("头含控制字符 %q 应失败", bad)
		}
	}

	cc512 := strings.Repeat("c", 512)
	if err := (Header{CacheControl: cc512}).Validate(); err != nil {
		t.Fatalf("Cache-Control 512 字节应通过: %v", err)
	}
	cc513 := strings.Repeat("c", 513)
	if err := (Header{CacheControl: cc513}).Validate(); err == nil {
		t.Fatal("Cache-Control 513 字节应失败")
	}
}

func TestJSON(t *testing.T) {
	resp, err := JSON(201, map[string]int{"a": 1})
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if resp.Status != 201 {
		t.Fatalf("Status = %d，期望 201", resp.Status)
	}
	wantBody, err := json.Marshal(map[string]int{"a": 1})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(resp.Body) != string(wantBody) {
		t.Fatalf("Body = %s，期望 %s", resp.Body, wantBody)
	}
	if resp.Header.ContentType != "application/json; charset=utf-8" {
		t.Fatalf("ContentType = %q", resp.Header.ContentType)
	}

	resp2, err := JSON(200, map[string]string{"x": "<script>"})
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(string(resp2.Body), "\\u003c") {
		t.Fatalf("含 < 的字符串应被转义为 \\u003c，得到 %s", resp2.Body)
	}
}

func TestResponseServe(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Cache-Control", "no-store")
	rec.Header().Set("X-Other", "1")

	resp := Response{
		Status: 200,
		Body:   []byte("ok"),
		Header: Header{ContentType: "text/plain", ETag: `"e1"`},
	}
	resp.Serve(rec)

	if rec.Code != 200 {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("ETag") != `"e1"` {
		t.Fatalf("ETag = %q", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control 应保留 no-store，得到 %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("X-Other") != "1" {
		t.Fatalf("X-Other 应保留")
	}

	rec204 := httptest.NewRecorder()
	Response{Status: 204}.Serve(rec204)
	if rec204.Code != 204 {
		t.Fatalf("204 状态码 = %d", rec204.Code)
	}
	if rec204.Body.Len() != 0 {
		t.Fatalf("204 不应写 body，得到 %q", rec204.Body.String())
	}
}
