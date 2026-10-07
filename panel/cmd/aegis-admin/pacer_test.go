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
