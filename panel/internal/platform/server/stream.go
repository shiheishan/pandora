package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// streamTracker 记下正在进行的事件流（SSE）请求，停机时一次性取消它们。
//
// 判定看响应而不是路径：处理函数第一次写响应头时 Content-Type 是
// text/event-stream，就是事件流。三个网关的事件流端点（门户、后台、节点）都这样
// 写，新增的流式端点不用回来登记；路径约定留给 middleware.Timeout 去管。
type streamTracker struct {
	mu       sync.Mutex
	draining bool
	next     uint64
	cancels  map[uint64]context.CancelFunc
}

func newStreamTracker() *streamTracker {
	return &streamTracker{cancels: make(map[uint64]context.CancelFunc)}
}

// register 登记一条事件流；已在停机中则立即取消它。返回注销函数。
func (t *streamTracker) register(cancel context.CancelFunc) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.draining {
		cancel()
		return func() {}
	}
	t.next++
	id := t.next
	t.cancels[id] = cancel
	return func() {
		t.mu.Lock()
		delete(t.cancels, id)
		t.mu.Unlock()
	}
}

// drain 进入停机：取消现有的全部事件流，之后新登记的也立即取消。返回取消的条数。
func (t *streamTracker) drain() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.draining = true
	n := len(t.cancels)
	for id, cancel := range t.cancels {
		cancel()
		delete(t.cancels, id)
	}
	return n
}

// wrap 给每个请求一个可单独取消的 ctx，并在响应被认出是事件流时登记它。
func (t *streamTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		sw := &streamAwareWriter{ResponseWriter: w, tracker: t, cancel: cancel}
		defer sw.release()
		next.ServeHTTP(sw, r.WithContext(ctx))
	})
}

// streamAwareWriter 在第一次写响应头时判断是不是事件流。
//
// 必须保留两种能力，否则事件流端点会坏：Flush（处理函数断言 http.Flusher）
// 与 Unwrap（http.NewResponseController 靠它找到底层连接去解除读写超时）。
type streamAwareWriter struct {
	http.ResponseWriter
	tracker    *streamTracker
	cancel     context.CancelFunc
	checked    bool
	unregister func()
}

func (w *streamAwareWriter) inspect() {
	if w.checked {
		return
	}
	w.checked = true
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.unregister = w.tracker.register(w.cancel)
	}
}

func (w *streamAwareWriter) release() {
	if w.unregister != nil {
		w.unregister()
	}
}

func (w *streamAwareWriter) WriteHeader(code int) {
	w.inspect()
	w.ResponseWriter.WriteHeader(code)
}

func (w *streamAwareWriter) Write(b []byte) (int, error) {
	w.inspect()
	return w.ResponseWriter.Write(b)
}

// Flush 实现 http.Flusher；底层不支持时静默忽略（与 ResponseController 的语义一致）。
func (w *streamAwareWriter) Flush() {
	w.inspect()
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap 让 http.NewResponseController 能穿透到底层 ResponseWriter。
func (w *streamAwareWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
