package node

import (
	"os"
	"strings"
	"testing"
)

func mustReadFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func between(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("missing %q", start)
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("missing %q after %q", end, start)
	}
	return rest[:j]
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func countOf(s, sub string) int { return strings.Count(s, sub) }
