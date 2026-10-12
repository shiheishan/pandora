package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	// HeaderReplayed 表示响应来自幂等重放。
	HeaderReplayed = "Idempotency-Replayed"
)

// Header 是会随重放返回的响应头（白名单）；空串表示没有。
type Header struct {
	ContentType, Location, ETag, CacheControl, ContentLanguage string
}

// Response 是一份可重放的成功响应。
type Response struct {
	Status int
	Body   []byte
	Header Header
}

// JSON 将 v 序列化为 JSON 响应。
func JSON(status int, v any) (Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Status: status,
		Body:   body,
		Header: Header{ContentType: "application/json; charset=utf-8"},
	}, nil
}

// Serve 把响应写入 ResponseWriter。
func (r Response) Serve(w http.ResponseWriter) {
	applyHeaderString(w.Header(), "Content-Type", r.Header.ContentType)
	applyHeaderString(w.Header(), "Location", r.Header.Location)
	applyHeaderString(w.Header(), "ETag", r.Header.ETag)
	applyHeaderString(w.Header(), "Cache-Control", r.Header.CacheControl)
	applyHeaderString(w.Header(), "Content-Language", r.Header.ContentLanguage)
	w.WriteHeader(r.Status)
	if len(r.Body) > 0 {
		_, _ = w.Write(r.Body)
	}
}

// Validate 校验白名单响应头的长度与字符规则。
func (h Header) Validate() error {
	checks := []struct {
		name  string
		value string
		max   int
	}{
		{"Content-Type", h.ContentType, 255},
		{"Location", h.Location, 2048},
		{"ETag", h.ETag, 255},
		{"Cache-Control", h.CacheControl, 512},
		{"Content-Language", h.ContentLanguage, 128},
	}
	for _, check := range checks {
		if check.value == "" {
			continue
		}
		if len(check.value) > check.max || containsHTTPControl(check.value) {
			return fmt.Errorf("idempotency: unsafe %s replay header", check.name)
		}
	}
	if h.Location != "" &&
		(!strings.HasPrefix(h.Location, "/") ||
			strings.HasPrefix(h.Location, "//") ||
			strings.Contains(h.Location, "\\")) {
		return errors.New("idempotency: unsafe Location replay header")
	}
	return nil
}

func applyHeaderString(header http.Header, name, value string) {
	if value == "" {
		return
	}
	header.Set(name, value)
}

func containsHTTPControl(value string) bool {
	for _, b := range []byte(value) {
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}
