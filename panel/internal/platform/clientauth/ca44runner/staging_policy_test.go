package ca44runner

import (
	"errors"
	"strings"
	"testing"
)

func TestPrivateStagePathGrammar(t *testing.T) {
	for _, value := range []string{"staging", "runtime/staging", "publish/releases"} {
		parts, err := splitRelativeLinux(value)
		if err != nil || len(parts) == 0 {
			t.Fatalf("valid relative path rejected: %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "/absolute", ".", "..", "a/../b", "a//b", "a\\b", "a\x00b", "a/"} {
		if _, err := splitRelativeLinux(value); err == nil {
			t.Fatalf("unsafe relative path accepted: %q", value)
		}
	}
}

func TestRandomRunNameGrammarAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for range 64 {
		name, err := randomRunName()
		if err != nil {
			t.Fatal(err)
		}
		if !validRunName(name) || !strings.HasPrefix(name, runPrefix) {
			t.Fatalf("invalid generated run name: %q", name)
		}
		if _, exists := seen[name]; exists {
			t.Fatalf("duplicate generated run name: %q", name)
		}
		seen[name] = struct{}{}
	}
	for _, value := range []string{"run.", "run.1234", "run.0123456789abcdef0123456789abcdeg", "RUN.0123456789abcdef0123456789abcdef", "run.0123456789abcdef0123456789abcdef/"} {
		if validRunName(value) {
			t.Fatalf("invalid run name accepted: %q", value)
		}
	}
}

func TestSyncVerifiedExistingBundleRequiresSuccessfulDurabilityBarrier(t *testing.T) {
	called := false
	err := syncVerifiedExistingBundle(41, 42, func(fds ...int) error {
		called = true
		if len(fds) != 2 || fds[0] != 41 || fds[1] != 42 {
			t.Fatalf("unexpected directory order: %v", fds)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("durability barrier did not succeed: called=%v err=%v", called, err)
	}

	sentinel := errors.New("fsync denied")
	if err := syncVerifiedExistingBundle(41, 42, func(...int) error { return sentinel }); err == nil {
		t.Fatal("fsync failure was accepted")
	}
	for _, test := range []struct {
		bundle, publish int
	}{
		{bundle: -1, publish: 42},
		{bundle: 41, publish: -1},
	} {
		if err := syncVerifiedExistingBundle(test.bundle, test.publish, func(...int) error { return nil }); err == nil {
			t.Fatalf("invalid descriptor accepted: %+v", test)
		}
	}
	if err := syncVerifiedExistingBundle(41, 42, nil); err == nil {
		t.Fatal("nil sync function accepted")
	}
}
