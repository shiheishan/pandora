package ca44runner

import (
	"errors"
	"testing"
)

func TestMergeRecoveryFailurePreservesCauseAndSurfacesQuarantineFailure(t *testing.T) {
	priorErr := errors.New("verifier denied")
	quarantineErr := errors.New("quarantine fsync denied")
	prior := &Failure{Kind: FailureVerifier, Stage: StageVerifier, Err: priorErr}

	merged := mergeRecoveryFailure(prior, quarantineErr)
	if merged == prior || merged.Kind != FailureIO || merged.Stage != StageRecovery {
		t.Fatalf("quarantine failure did not become authoritative: %+v", merged)
	}
	if !errors.Is(merged, priorErr) || !errors.Is(merged, quarantineErr) {
		t.Fatalf("failure causes were not retained: %v", merged)
	}
	if merged.ExitCode() != 74 || merged.SanitizedLine() != "root_runner=DENY stage=recovery reason=io_failure\n" {
		t.Fatalf("unexpected public failure contract: code=%d line=%q", merged.ExitCode(), merged.SanitizedLine())
	}

	onlyQuarantine := mergeRecoveryFailure(nil, quarantineErr)
	if onlyQuarantine.Kind != FailureIO || onlyQuarantine.Stage != StageRecovery ||
		!errors.Is(onlyQuarantine, quarantineErr) {
		t.Fatalf("standalone quarantine failure mismatch: %+v", onlyQuarantine)
	}
	if unchanged := mergeRecoveryFailure(prior, nil); unchanged != prior {
		t.Fatal("nil quarantine error changed existing failure")
	}
}
