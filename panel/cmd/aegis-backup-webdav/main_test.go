package main

import "testing"

func TestRunRefusesUnexpectedArguments(t *testing.T) {
	if got := run(nil); got != 2 {
		t.Fatalf("run(nil)=%d", got)
	}
	if got := run([]string{"relative", "relative.sha256"}); got != 1 {
		t.Fatalf("run(invalid pair)=%d", got)
	}
}
