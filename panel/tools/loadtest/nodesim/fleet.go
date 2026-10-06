// [INPUT]: 依赖 tools/loadtest/ltkit 的 Recorder 与 Observation，sync/atomic
// [OUTPUT]: 对外提供 包内 observer（record、sample）、fleetStats（streamOpened、countEvent、snapshot）、fleetSummary、progressLine
// [POS]: tools/loadtest/nodesim 的计量接线：请求进 ltkit.Recorder（同一口径的延迟与错误码），Recorder 装不下的整机状态（在线流数、事件计数、验签失败、未起来的节点）进 fleetStats，最后作为 meta 写进 nodes.json
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodesim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// observer 把一次请求记进 Recorder。自己取消造成的中断（到时停机、Ctrl-C）
// 不记：那不是面板的错，记进去会在每次收尾时凭空多出一批 transport 错误。
type observer struct {
	rec   *ltkit.Recorder
	fleet *fleetStats
	log   io.Writer
	// 错误样本只打前若干条：两百个节点同时报同一个错，刷屏没有信息量
	samples    atomic.Int64
	maxSamples int64
}

func (o *observer) record(ctx context.Context, endpoint string, status int, latency time.Duration, err error, flag string) {
	if status == 0 && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return
	}
	o.rec.Observe(ltkit.Observation{Endpoint: endpoint, Status: status, Latency: latency, Err: err, Flag: flag})
}

// sample 打一条节点侧错误（pdnd 会写日志的那些），只打前 maxSamples 条。
func (o *observer) sample(what, nodeID string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	o.fleet.nodeErrors.Add(1)
	if o.samples.Add(1) > o.maxSamples || o.log == nil {
		return
	}
	fmt.Fprintf(o.log, "nodes: node %s %s: %v\n", nodeID, what, err)
}

// fleetStats 是全体模拟节点共享的计数，全部原子操作。
type fleetStats struct {
	launched         atomic.Int64
	started          atomic.Int64
	streamsOpen      atomic.Int64
	streamsPeak      atomic.Int64
	streamDrops      atomic.Int64
	configApplied    atomic.Int64
	verifyFailures   atomic.Int64
	keyTransitions   atomic.Int64
	userListsApplied atomic.Int64
	deltaMismatches  atomic.Int64
	nodeErrors       atomic.Int64

	mu     sync.Mutex
	events map[string]int64
}

func newFleetStats() *fleetStats { return &fleetStats{events: map[string]int64{}} }

// streamOpened 记一条流建立，顺手刷新同时在线的峰值（收尾时流都已断开，
// 只看结束那一刻的在线数是零，没有意义）。
func (f *fleetStats) streamOpened() {
	now := f.streamsOpen.Add(1)
	for {
		peak := f.streamsPeak.Load()
		if now <= peak || f.streamsPeak.CompareAndSwap(peak, now) {
			return
		}
	}
}

func (f *fleetStats) countEvent(kind string) {
	f.mu.Lock()
	f.events[kind]++
	f.mu.Unlock()
}

// fleetSummary 是一次运行结束时的整机状态，进 nodes.json 的 meta。
type fleetSummary struct {
	Launched         int64            `json:"launched"`
	Started          int64            `json:"started"`
	NotStarted       []string         `json:"not_started,omitempty"`
	StreamsOpen      int64            `json:"streams_open"`
	StreamsPeak      int64            `json:"streams_peak"`
	StreamDrops      int64            `json:"stream_drops"`
	StreamEvents     map[string]int64 `json:"stream_events"`
	ConfigApplied    int64            `json:"config_applied"`
	VerifyFailures   int64            `json:"config_verify_failures"`
	KeyTransitions   int64            `json:"config_key_transitions"`
	UserListsApplied int64            `json:"user_lists_applied"`
	DeltaMismatches  int64            `json:"delta_mismatches"`
	NodeErrors       int64            `json:"node_errors"`
}

func (f *fleetStats) snapshot() fleetSummary {
	f.mu.Lock()
	events := make(map[string]int64, len(f.events))
	for k, v := range f.events {
		events[k] = v
	}
	f.mu.Unlock()
	return fleetSummary{
		Launched: f.launched.Load(), Started: f.started.Load(),
		StreamsOpen: f.streamsOpen.Load(), StreamsPeak: f.streamsPeak.Load(), StreamDrops: f.streamDrops.Load(), StreamEvents: events,
		ConfigApplied: f.configApplied.Load(), VerifyFailures: f.verifyFailures.Load(),
		KeyTransitions: f.keyTransitions.Load(), UserListsApplied: f.userListsApplied.Load(),
		DeltaMismatches: f.deltaMismatches.Load(), NodeErrors: f.nodeErrors.Load(),
	}
}

// progressLine 是每分钟打到 stdout 的一行。
func progressLine(elapsed time.Duration, total int, rep ltkit.Report, s fleetSummary) string {
	t := rep.Totals
	return fmt.Sprintf("nodes t=%s launched=%d/%d started=%d streams=%d req=%d qps=%.1f p99=%.1fms 5xx=%d sig_fail=%d auth_fail=%d 304=%d transport=%d verify_fail=%d events=%v",
		elapsed.Round(time.Second), s.Launched, total, s.Started, s.StreamsOpen, t.Count, t.QPS, t.P99MS,
		t.Server5x, t.Flags[flagSigFail], t.Flags[flagAuthFail], t.Flags[flagETag304], transportErrors(t.Codes),
		s.VerifyFailures, s.StreamEvents)
}

func transportErrors(codes map[string]uint64) uint64 {
	var n uint64
	for k, v := range codes {
		if strings.HasPrefix(k, "transport") {
			n += v
		}
	}
	return n
}
