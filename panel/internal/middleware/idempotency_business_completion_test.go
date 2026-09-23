package middleware

import "testing"

// 业务在自己的事务里完成幂等记录之后，中间件不该再判成异常。
//
// 这个测试守的是一个具体的坑：SecurityHeaders 给所有 API 响应加
// Cache-Control: no-store，而业务写记录时（idempotencybind.CompleteSuccessJSON）
// 响应还没经过它，库里那一列因此是 NULL。中间件事后捕获到的是加过头的最终
// 响应，两边永远差这一项——每次下单都判成 resource_bound 并记一条 CAS
// rejected，生产上累计了 44 条，而记录其实完整无缺。
//
// 所以判定只比业务真正决定的头。谁要把 Cache-Control 加回比较里，这个测试
// 会先失败。
func TestBusinessCompletionIgnoresTransportCacheControl(t *testing.T) {
	payload := []byte(`{"order_id":"019ff216-2d2d-7a4a-bd9a-adfa2c9d4d66"}`)
	code := 201
	contentType := "application/json; charset=utf-8"
	noStore := "no-store"

	// 业务写进库的那一份：没有 Cache-Control。
	record := idempotencyRecord{
		status:          "succeeded",
		generation:      1,
		responseCode:    &code,
		responseFormat:  "bytes",
		responsePayload: payload,
		headers:         idempotencyReplayHeaders{ContentType: &contentType},
		resourceType:    stringPointer("order"),
		resourceID:      stringPointer("019ff216-2d2d-7a4a-bd9a-adfa2c9d4d66"),
	}
	// 中间件捕获到的最终响应：SecurityHeaders 已经把 no-store 加上了。
	expected := idempotencyCompletion{
		status:          "succeeded",
		responseCode:    code,
		responseFormat:  "bytes",
		responsePayload: payload,
		headers: idempotencyReplayHeaders{
			ContentType:  &contentType,
			CacheControl: &noStore,
		},
	}

	if got := classifyIdempotencyCompletionProbe(record, 1, expected); got != "business_completed" {
		t.Fatalf("业务已完成的记录被判成 %q，期望 business_completed", got)
	}

	// 业务真正决定的头如果对不上，仍然要拦下来——放宽只针对传输层那一项。
	otherType := "text/plain"
	mismatched := expected
	mismatched.headers.ContentType = &otherType
	if got := classifyIdempotencyCompletionProbe(record, 1, mismatched); got == "business_completed" {
		t.Fatal("Content-Type 不一致却判成 business_completed，放宽过头了")
	}
}
