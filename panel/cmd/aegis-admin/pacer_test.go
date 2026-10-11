package main

import (
	"testing"
	"time"
)

func TestLoopPacerSpreadsFirstRunAndJittersEveryRound(t *testing.T) {
	for i := 0; i < 1000; i++ {
		d := randDuration(firstDelayCap)
		if d < 0 || d >= firstDelayCap {
			t.Fatalf("first delay %v out of [0, %v)", d, firstDelayCap)
		}
		j := jittered(10 * time.Minute)
		if j < 9*time.Minute || j >= 11*time.Minute {
			t.Fatalf("jittered interval %v out of [9m, 11m)", j)
		}
	}
	// 抖动确实在变，不是常数
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[jittered(time.Hour)] = true
	}
	if len(seen) < 10 {
		t.Fatalf("jitter looks constant: %d distinct values", len(seen))
	}
}

func TestLoopPacerFiresFirstWithinIntervalThenRearms(t *testing.T) {
	p := newLoopPacer(20 * time.Millisecond)
	defer p.Stop()
	for round := 0; round < 3; round++ {
		select {
		case <-p.C():
		case <-time.After(time.Second):
			t.Fatalf("round %d never fired", round)
		}
	}
}

func TestIntervalGateRunsOnceAnHourOnTenMinuteTicks(t *testing.T) {
	g := newIntervalGate(time.Hour, 10*time.Minute)
	t0 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	if !g.due(t0) {
		t.Fatal("a fresh gate must be due on the first tick")
	}
	g.done(t0)
	// 节拍带 ±10% 抖动：之后每拍 9 到 11 分钟，到点的应该是第 6 拍附近，而不是第 5 或第 7
	for _, tc := range []struct {
		after time.Duration
		want  bool
	}{
		{10 * time.Minute, false}, {20 * time.Minute, false}, {40 * time.Minute, false},
		{49 * time.Minute, false}, {54*time.Minute + 59*time.Second, false},
		{55 * time.Minute, true}, {60 * time.Minute, true}, {66 * time.Minute, true},
	} {
		if got := g.due(t0.Add(tc.after)); got != tc.want {
			t.Fatalf("due(+%s) = %v, want %v", tc.after, got, tc.want)
		}
	}
	// 没有成功就不记：下一拍还是到点
	if !g.due(t0.Add(70 * time.Minute)) {
		t.Fatal("a failed round must be retried on the next tick")
	}
	g.done(t0.Add(66 * time.Minute))
	if g.due(t0.Add(76 * time.Minute)) {
		t.Fatal("gate reopened ten minutes after a successful round")
	}
}
