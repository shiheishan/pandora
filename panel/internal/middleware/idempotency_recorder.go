// [INPUT]: 依赖 idempotency.go 的 idempotencyCompletion 与 idempotency_replay.go 的响应头捕获
// [OUTPUT]: 包内提供 recorder 与 newIdempotencyRecorder，以及状态码与 Content-Type 的准入判断
// [POS]: middleware 幂等键的响应录制器：从 idempotency.go 拆出。把业务处理器写出的状态码、白名单响应头与正文同时透传给客户端并留存，供事务内完成认领；响应头不合规、带 Content-Encoding / Transfer-Encoding 时作废留存（照常透传），重复 WriteHeader 忽略，状态码越界直接 panic
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"bytes"
	"fmt"
	"net/http"
)

type recorder struct {
	http.ResponseWriter
	status             int
	body               bytes.Buffer
	wroteHeader        bool
	committedHeader    http.Header
	contentType        string
	contentTypePresent bool
	sniffContentType   bool
	captureComplete    bool
	headers            idempotencyReplayHeaders
}

func newIdempotencyRecorder(w http.ResponseWriter) *recorder {
	return &recorder{ResponseWriter: w, captureComplete: true}
}

func (r *recorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	checkIdempotencyWriteHeaderCode(code)
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	r.status = code
	r.wroteHeader = true
	r.committedHeader = r.Header().Clone()
	r.contentType, r.contentTypePresent = idempotencyContentType(r.committedHeader)
	contentEncoding := r.committedHeader.Get("Content-Encoding")
	transferEncoding := r.committedHeader.Get("Transfer-Encoding")
	_, contentEncodingPresent := r.committedHeader[http.CanonicalHeaderKey("Content-Encoding")]
	_, transferEncodingPresent := r.committedHeader[http.CanonicalHeaderKey("Transfer-Encoding")]

	var captureErr error
	r.headers.ContentType, captureErr = captureSingleHeader(r.committedHeader, "Content-Type")
	if captureErr == nil {
		r.headers.Location, captureErr = captureSingleHeader(r.committedHeader, "Location")
	}
	if captureErr == nil {
		r.headers.ETag, captureErr = captureSingleHeader(r.committedHeader, "ETag")
	}
	if captureErr == nil {
		r.headers.CacheControl, captureErr = captureSingleHeader(r.committedHeader, "Cache-Control")
	}
	if captureErr == nil {
		r.headers.ContentLanguage, captureErr = captureSingleHeader(r.committedHeader, "Content-Language")
	}
	if captureErr == nil {
		captureErr = validateIdempotencyReplayHeaders(r.headers)
	}
	if captureErr != nil || contentEncodingPresent || transferEncodingPresent {
		r.invalidateCapture()
	}
	r.sniffContentType = r.captureComplete && idempotencyBodyAllowedForStatus(code) &&
		!r.contentTypePresent && contentEncoding == "" && transferEncoding == ""
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if len(b) > 0 && !idempotencyBodyAllowedForStatus(r.status) {
		r.invalidateCapture()
	}
	n, err := r.ResponseWriter.Write(b)
	if r.captureComplete {
		if err != nil || n != len(b) {
			r.invalidateCapture()
		} else if n > maxIdempotencyResponseBytes-r.body.Len() {
			r.invalidateCapture()
		} else {
			_, _ = r.body.Write(b[:n])
		}
	}
	return n, err
}

func (r *recorder) finish() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if r.captureComplete && r.sniffContentType && r.body.Len() > 0 {
		r.contentType = http.DetectContentType(r.body.Bytes())
		r.contentTypePresent = true
		r.headers.ContentType = stringPointer(r.contentType)
	}
}

func (r *recorder) idempotencyCompletion(status string) idempotencyCompletion {
	completion := idempotencyCompletion{
		status: status, responseCode: r.status, responseFormat: "unavailable",
	}
	if !r.captureComplete {
		return completion
	}
	completion.responseFormat = "bytes"
	completion.responsePayload = append([]byte{}, r.body.Bytes()...)
	completion.headers = cloneIdempotencyReplayHeaders(r.headers)
	return completion
}

func (r *recorder) invalidateCapture() {
	r.captureComplete = false
	r.body.Reset()
	r.headers = idempotencyReplayHeaders{}
}

func checkIdempotencyWriteHeaderCode(code int) {
	if code < 100 || code > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", code))
	}
}

func idempotencyBodyAllowedForStatus(code int) bool {
	return code >= http.StatusOK && code != http.StatusNoContent &&
		code != http.StatusNotModified
}

func idempotencyContentType(header http.Header) (string, bool) {
	values, present := header[http.CanonicalHeaderKey("Content-Type")]
	if !present || len(values) == 0 {
		return "", present
	}
	return values[0], true
}
