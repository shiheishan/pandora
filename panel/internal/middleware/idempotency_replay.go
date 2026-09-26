// [INPUT]: 依赖 idempotency.go 的 idempotencyRecord、storedResponseEnvelope 与 idempotencyReplayHeaders，依赖 platform/httpx 的错误模型
// [OUTPUT]: 包内提供 decideIdempotencyRecord / decideIdempotencyReplay 重放判定、存储响应的编解码、serveIdempotencyReplayDecision 重放出口，以及重放响应头的复制、比对、校验与写回
// [POS]: middleware 幂等键的重放半边：从 idempotency.go 拆出。按认领记录的状态（in_flight / succeeded / failed，状态与响应码不自洽即报错）和存储格式（bytes 与两种旧格式）决定重放什么；只保存并重放白名单里的五个业务响应头（Content-Type、Location、ETag、Cache-Control、Content-Language），写回前校验长度与控制字符
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type idempotencyReplayDecision struct {
	replay          bool
	retryable       bool
	terminalBlocked bool
	status          int
	body            []byte
	headers         idempotencyReplayHeaders
	// Compatibility fields retained for the focused legacy-envelope unit tests.
	contentType        string
	contentTypePresent bool
}

func decideIdempotencyRecord(record idempotencyRecord) (idempotencyReplayDecision, error) {
	if record.generation <= 0 {
		return idempotencyReplayDecision{}, errors.New("idempotency: invalid claim_generation")
	}
	if (record.resourceType == nil) != (record.resourceID == nil) {
		return idempotencyReplayDecision{}, errors.New("idempotency: partial resource binding")
	}
	resourceBound := record.resourceType != nil

	switch record.status {
	case "in_flight":
		if record.responseCode != nil || record.responseFormat != "none" ||
			record.responseBody != nil || record.responsePayload != nil ||
			!headersEmpty(record.headers) || record.lockedUntil == nil {
			return idempotencyReplayDecision{}, errors.New("idempotency: malformed in_flight evidence")
		}
		return idempotencyReplayDecision{}, nil
	case "succeeded", "failed":
		if record.lockedUntil != nil || record.responseCode == nil {
			return idempotencyReplayDecision{}, errors.New("idempotency: malformed terminal evidence")
		}
		is2xx := *record.responseCode >= http.StatusOK &&
			*record.responseCode < http.StatusMultipleChoices
		if record.status == "succeeded" && !is2xx {
			return idempotencyReplayDecision{}, fmt.Errorf(
				"idempotency: succeeded record has non-2xx response_code %d", *record.responseCode,
			)
		}
		if record.status == "failed" && is2xx {
			return idempotencyReplayDecision{}, fmt.Errorf(
				"idempotency: failed record has 2xx response_code %d", *record.responseCode,
			)
		}
	default:
		return idempotencyReplayDecision{}, fmt.Errorf(
			"idempotency: unsupported record status %q", record.status,
		)
	}

	decision := idempotencyReplayDecision{status: *record.responseCode}
	switch record.responseFormat {
	case "bytes":
		if record.responseBody != nil || record.responsePayload == nil {
			return idempotencyReplayDecision{}, errors.New("idempotency: malformed bytes response")
		}
		if err := validateIdempotencyReplayHeaders(record.headers); err != nil {
			return idempotencyReplayDecision{}, err
		}
		decision.body = append([]byte(nil), record.responsePayload...)
		decision.headers = cloneIdempotencyReplayHeaders(record.headers)
	case "legacy_json":
		if record.responseBody == nil || record.responsePayload != nil || !headersEmpty(record.headers) {
			return idempotencyReplayDecision{}, errors.New("idempotency: malformed legacy_json response")
		}
		body, contentType, contentTypePresent, err := decodeStoredIdempotencyResponse(record.responseBody)
		if err != nil {
			return idempotencyReplayDecision{}, err
		}
		decision.body = body
		if contentTypePresent {
			decision.headers.ContentType = stringPointer(contentType)
			decision.contentType = contentType
			decision.contentTypePresent = true
		}
	case "legacy_empty":
		if record.responseBody != nil || record.responsePayload != nil || !headersEmpty(record.headers) {
			return idempotencyReplayDecision{}, errors.New("idempotency: malformed legacy_empty response")
		}
		decision.headers.ContentType = stringPointer("application/json; charset=utf-8")
		decision.contentType = "application/json; charset=utf-8"
		decision.contentTypePresent = true
	case "unavailable":
		if record.responseBody != nil || record.responsePayload != nil || !headersEmpty(record.headers) {
			return idempotencyReplayDecision{}, errors.New("idempotency: malformed unavailable response")
		}
		decision.terminalBlocked = true
		return decision, nil
	default:
		return idempotencyReplayDecision{}, fmt.Errorf(
			"idempotency: unknown response_format %q", record.responseFormat,
		)
	}

	if record.status == "succeeded" {
		decision.replay = true
		return decision, nil
	}
	decision.retryable = !resourceBound &&
		(record.responseFormat == "bytes" || record.responseFormat == "legacy_empty")
	decision.terminalBlocked = !decision.retryable
	return decision, nil
}

// decideIdempotencyReplay preserves the historical decoder test surface. New
// runtime rows are decided by decideIdempotencyRecord.
func decideIdempotencyReplay(
	status string, code *int, stored []byte,
) (idempotencyReplayDecision, error) {
	format := "legacy_json"
	if stored == nil {
		format = "legacy_empty"
	}
	return decideIdempotencyRecord(idempotencyRecord{
		status: status, responseCode: code, responseFormat: format,
		responseBody: stored, generation: 1,
	})
}

func decodeStoredIdempotencyResponse(stored []byte) ([]byte, string, bool, error) {
	if stored == nil {
		return nil, "application/json; charset=utf-8", true, nil
	}

	var envelope storedResponseEnvelope
	if err := json.Unmarshal(stored, &envelope); err == nil &&
		envelope.Envelope == storedResponseEnvelopeVersion {
		if !envelope.CaptureComplete {
			return nil, "", false, errors.New(
				"idempotency: successful response capture is incomplete; refusing replay",
			)
		}
		body, err := base64.StdEncoding.DecodeString(envelope.BodyBase64)
		if err != nil {
			return nil, "", false, fmt.Errorf("idempotency: invalid response envelope: %w", err)
		}
		contentTypePresent := envelope.ContentTypePresent || envelope.ContentType != ""
		return body, envelope.ContentType, contentTypePresent, nil
	}

	if !json.Valid(stored) {
		return nil, "", false, errors.New("idempotency: malformed historical response_body")
	}
	return append([]byte(nil), stored...), "application/json; charset=utf-8", true, nil
}

// encodeStoredIdempotencyResponse is legacy-only and retained for decoder
// compatibility tests. The runtime never writes this JSON sentinel on schema37.
func encodeStoredIdempotencyResponse(rec *recorder) ([]byte, error) {
	envelope := storedResponseEnvelope{
		Envelope:           storedResponseEnvelopeVersion,
		BodyBase64:         base64.StdEncoding.EncodeToString(rec.body.Bytes()),
		ContentType:        rec.contentType,
		ContentTypePresent: rec.contentTypePresent,
		CaptureComplete:    rec.captureComplete,
	}
	if !rec.captureComplete {
		envelope.BodyBase64 = ""
	}
	return json.Marshal(envelope)
}

func serveIdempotencyReplayDecision(w http.ResponseWriter, decision idempotencyReplayDecision) error {
	if !decision.replay {
		return errors.New("idempotency: non-replay decision passed to replay writer")
	}
	w.Header().Set("Idempotency-Replayed", "true")
	applyIdempotencyReplayHeaders(w.Header(), decision.headers)
	w.WriteHeader(decision.status)
	if len(decision.body) > 0 {
		_, _ = w.Write(decision.body)
	}
	return nil
}

// serveSucceededIdempotencyReplay retains the historical unit-test API.
func serveSucceededIdempotencyReplay(
	w http.ResponseWriter, code *int, body []byte,
	contentType string, contentTypePresent bool,
) error {
	if code == nil {
		return errors.New("idempotency: succeeded record has no response_code")
	}
	decision := idempotencyReplayDecision{replay: true, status: *code, body: body}
	if contentTypePresent {
		decision.headers.ContentType = stringPointer(contentType)
	}
	return serveIdempotencyReplayDecision(w, decision)
}

func stringPointer(value string) *string {
	copyValue := value
	return &copyValue
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}

func cloneIdempotencyReplayHeaders(headers idempotencyReplayHeaders) idempotencyReplayHeaders {
	return idempotencyReplayHeaders{
		ContentType:     cloneStringPointer(headers.ContentType),
		Location:        cloneStringPointer(headers.Location),
		ETag:            cloneStringPointer(headers.ETag),
		CacheControl:    cloneStringPointer(headers.CacheControl),
		ContentLanguage: cloneStringPointer(headers.ContentLanguage),
	}
}

func headersEmpty(headers idempotencyReplayHeaders) bool {
	return headers.ContentType == nil && headers.Location == nil && headers.ETag == nil &&
		headers.CacheControl == nil && headers.ContentLanguage == nil
}

// businessHeadersEqual 比较业务能决定的那几个响应头，忽略 Cache-Control。
//
// 幂等记录是业务在自己的事务里写的（idempotencybind.CompleteSuccessJSON），
// 那时响应还没经过 SecurityHeaders——那个中间件给所有 API 响应统一加
// Cache-Control: no-store。而中间件事后捕获到的是加过头的最终响应，于是
// 两边永远差这一项：库里是 NULL，捕获到的是 no-store。
//
// 后果是每次下单都判成 resource_bound、报一条 CAS rejected，而记录其实是
// 完整的。生产上累计了 44 条这样的 ERROR，直到有人去查才发现是自己比自己
// 比不过。
//
// 用完整的 headersEqual 去比是不可能满足的要求：业务在事务内完成，传输层
// 的头在事务外才确定。所以这里只比业务真正决定的部分——Cache-Control 由
// 中间件统一保证，重放时同样会被加上，不属于业务响应语义。
func businessHeadersEqual(a, b idempotencyReplayHeaders) bool {
	return stringPointersEqual(a.ContentType, b.ContentType) &&
		stringPointersEqual(a.Location, b.Location) &&
		stringPointersEqual(a.ETag, b.ETag) &&
		stringPointersEqual(a.ContentLanguage, b.ContentLanguage)
}

func headersEqual(a, b idempotencyReplayHeaders) bool {
	return stringPointersEqual(a.ContentType, b.ContentType) &&
		stringPointersEqual(a.Location, b.Location) &&
		stringPointersEqual(a.ETag, b.ETag) &&
		stringPointersEqual(a.CacheControl, b.CacheControl) &&
		stringPointersEqual(a.ContentLanguage, b.ContentLanguage)
}

func stringPointersEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func applyIdempotencyReplayHeaders(header http.Header, headers idempotencyReplayHeaders) {
	applyOptionalHeader(header, "Content-Type", headers.ContentType)
	applyOptionalHeader(header, "Location", headers.Location)
	applyOptionalHeader(header, "ETag", headers.ETag)
	applyOptionalHeader(header, "Cache-Control", headers.CacheControl)
	applyOptionalHeader(header, "Content-Language", headers.ContentLanguage)
}

func applyOptionalHeader(header http.Header, name string, value *string) {
	canonical := http.CanonicalHeaderKey(name)
	if value == nil {
		header.Del(canonical)
		return
	}
	header[canonical] = []string{*value}
}

func validateIdempotencyReplayHeaders(headers idempotencyReplayHeaders) error {
	checks := []struct {
		name  string
		value *string
		max   int
	}{
		{"Content-Type", headers.ContentType, 255},
		{"Location", headers.Location, 2048},
		{"ETag", headers.ETag, 255},
		{"Cache-Control", headers.CacheControl, 512},
		{"Content-Language", headers.ContentLanguage, 128},
	}
	for _, check := range checks {
		if check.value == nil {
			continue
		}
		if len(*check.value) > check.max || containsHTTPControl(*check.value) {
			return fmt.Errorf("idempotency: unsafe %s replay header", check.name)
		}
	}
	if headers.Location != nil &&
		(!strings.HasPrefix(*headers.Location, "/") ||
			strings.HasPrefix(*headers.Location, "//") ||
			strings.Contains(*headers.Location, "\\")) {
		return errors.New("idempotency: unsafe Location replay header")
	}
	return nil
}

func containsHTTPControl(value string) bool {
	for _, b := range []byte(value) {
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}

func captureSingleHeader(header http.Header, name string) (*string, error) {
	values, present := header[http.CanonicalHeaderKey(name)]
	if !present {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("idempotency: %s has multiple values", name)
	}
	return stringPointer(values[0]), nil
}
