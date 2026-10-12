package handler

import (
	"bytes"
	"net/http"

	"github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/gin-gonic/gin"
)

// recorder 包住 gin.ResponseWriter，在 handler 写响应时录下状态码、体与白名单头供幂等完成入库。
type recorder struct {
	gin.ResponseWriter
	body       bytes.Buffer
	header     http.Header
	incomplete bool
}

func (r *recorder) snapshot() {
	if r.header != nil {
		return
	}
	r.header = r.ResponseWriter.Header().Clone()
}

func (r *recorder) invalidate() {
	r.incomplete = true
	r.body.Reset()
}

func bodyAllowedForStatus(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
}

func (r *recorder) Write(b []byte) (int, error) {
	r.snapshot()
	if len(b) > 0 && !bodyAllowedForStatus(r.ResponseWriter.Status()) {
		r.invalidate()
	}
	n, err := r.ResponseWriter.Write(b)
	if r.incomplete {
		return n, err
	}
	if err != nil || n != len(b) {
		r.invalidate()
		return n, err
	}
	if r.body.Len()+n > service.MaxBodyBytes {
		r.invalidate()
		return n, err
	}
	_, _ = r.body.Write(b[:n])
	return n, err
}

func (r *recorder) WriteString(s string) (int, error) {
	r.snapshot()
	if len(s) > 0 && !bodyAllowedForStatus(r.ResponseWriter.Status()) {
		r.invalidate()
	}
	n, err := r.ResponseWriter.WriteString(s)
	if r.incomplete {
		return n, err
	}
	if err != nil || n != len(s) {
		r.invalidate()
		return n, err
	}
	if r.body.Len()+n > service.MaxBodyBytes {
		r.invalidate()
		return n, err
	}
	_, _ = r.body.WriteString(s[:n])
	return n, err
}

func whitelistHeader(hdr http.Header, name string) (string, bool) {
	values := hdr[http.CanonicalHeaderKey(name)]
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func (r *recorder) result() (status int, resp service.Response, replayable bool) {
	r.snapshot()
	status = r.ResponseWriter.Status()
	if r.incomplete {
		return status, service.Response{}, false
	}
	if r.header == nil {
		return status, service.Response{}, false
	}
	if _, encPresent := r.header[http.CanonicalHeaderKey("Content-Encoding")]; encPresent {
		return status, service.Response{}, false
	}
	if _, tePresent := r.header[http.CanonicalHeaderKey("Transfer-Encoding")]; tePresent {
		return status, service.Response{}, false
	}

	var h service.Header
	names := []struct {
		field string
		set   func(string)
	}{
		{"Content-Type", func(v string) { h.ContentType = v }},
		{"Location", func(v string) { h.Location = v }},
		{"ETag", func(v string) { h.ETag = v }},
		{"Cache-Control", func(v string) { h.CacheControl = v }},
		{"Content-Language", func(v string) { h.ContentLanguage = v }},
	}
	for _, item := range names {
		v, ok := whitelistHeader(r.header, item.field)
		if !ok {
			return status, service.Response{}, false
		}
		if v != "" {
			item.set(v)
		}
	}
	if err := h.Validate(); err != nil {
		return status, service.Response{}, false
	}

	body := append([]byte{}, r.body.Bytes()...)
	if h.ContentType == "" && len(body) > 0 && bodyAllowedForStatus(status) {
		h.ContentType = http.DetectContentType(body)
		if err := h.Validate(); err != nil {
			return status, service.Response{}, false
		}
	}

	resp = service.Response{
		Status: status,
		Body:   body,
		Header: h,
	}
	return status, resp, true
}
