package nodesim

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// current 照 pdnd：没送到的那份原样重发、带同一个 X-Report-Id，期间的新流量按 uid
// 合并，在同一轮紧接着报。
func TestCurrentPushRetriesVerbatimWithSameReportID(t *testing.T) {
	type push struct {
		id   string
		body map[string][2]int64
	}
	var mu sync.Mutex
	var pushes []push
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string][2]int64
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		defer mu.Unlock()
		pushes = append(pushes, push{id: r.Header.Get(reportIDHeader), body: body})
		if len(pushes) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	obs := &observer{rec: ltkit.NewRecorder("nodes", time.Second), fleet: newFleetStats(), maxSamples: 1}
	n := &simNode{id: "n1", opt: &Options{}, obs: obs,
		uni: newUniClient(srv.URL, "n1", "vless", "tok", "", time.Second, obs)}

	n.flushPush(context.Background(), map[string][2]int64{"1": {10, 20}})
	n.flushPush(context.Background(), map[string][2]int64{"1": {1, 1}, "2": {5, 5}})
	if len(pushes) != 3 {
		t.Fatalf("上报 %d 次，期望 3 次", len(pushes))
	}
	if pushes[0].id == "" || pushes[1].id != pushes[0].id || pushes[1].body["1"] != [2]int64{10, 20} {
		t.Fatalf("重发不是原样、同 ID：%+v", pushes[:2])
	}
	if pushes[2].id == pushes[0].id || pushes[2].body["2"] != [2]int64{5, 5} {
		t.Fatalf("新流量那一份不对：%+v", pushes[2])
	}
}
