//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProductionV3ComposerRejectsInvalidRoutingBeforeAcquisition(t *testing.T) {
	for _, test := range []struct {
		ctx       context.Context
		attemptID string
	}{
		{nil, "attempt-ca42-1"},
		{context.Background(), ""},
		{context.Background(), "../escape"},
		{context.Background(), strings.Repeat("a", 129)},
	} {
		session, err := OpenProductionV3VerificationSession(test.ctx, test.attemptID)
		if session != nil || !errors.Is(err, errProductionV3ComposerUnavailable) {
			t.Fatalf("invalid production route accepted: session=%v err=%v", session, err)
		}
	}
}

func TestProductionV3SHA256RequiresCanonicalLowercase(t *testing.T) {
	valid := strings.Repeat("a", 64)
	if _, err := productionV3SHA256(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 66), strings.Repeat("z", 64)} {
		if _, err := productionV3SHA256(invalid); !errors.Is(err, errProductionV3ComposerUnavailable) {
			t.Fatalf("non-canonical digest accepted or misclassified: %q %v", invalid, err)
		}
	}
}
