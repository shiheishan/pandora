package ca44runner

import (
	"errors"
	"fmt"
)

type FailureKind uint8

const (
	FailureUsage FailureKind = iota + 1
	FailureInternal
	FailureIO
	FailureTrust
	FailureClassifier
	FailureVerifier
	FailureInterrupted
)

type FailureStage string

const (
	StageBootstrap  FailureStage = "bootstrap"
	StageRecovery   FailureStage = "recovery"
	StageManifest   FailureStage = "manifest"
	StageIdentity   FailureStage = "identity"
	StageInputs     FailureStage = "inputs"
	StageClassifier FailureStage = "classifier"
	StageVerifier   FailureStage = "verifier"
	StagePublish    FailureStage = "publish"
)

type Failure struct {
	Kind   FailureKind
	Stage  FailureStage
	Signal int
	Err    error
}

// mergeRecoveryFailure makes a failed quarantine the externally visible
// failure while retaining the earlier cause for local error inspection. A
// caller must never report only the original stage when private residue could
// not be moved into quarantine.
func mergeRecoveryFailure(existing *Failure, quarantineErr error) *Failure {
	if quarantineErr == nil {
		return existing
	}
	if existing == nil {
		return &Failure{Kind: FailureIO, Stage: StageRecovery, Err: quarantineErr}
	}
	return &Failure{
		Kind:  FailureIO,
		Stage: StageRecovery,
		Err:   errors.Join(existing.Err, quarantineErr),
	}
}

func (f *Failure) Error() string {
	if f == nil || f.Err == nil {
		return "root runner failure"
	}
	return f.Err.Error()
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

func (f *Failure) ExitCode() int {
	if f == nil {
		return 0
	}
	switch f.Kind {
	case FailureUsage:
		return 64
	case FailureInternal:
		return 70
	case FailureIO:
		return 74
	case FailureInterrupted:
		if f.Signal >= 1 && f.Signal <= 64 {
			return 128 + f.Signal
		}
		return 130
	default:
		return 78
	}
}

func (f *Failure) SanitizedLine() string {
	if f == nil {
		return "root_runner=DENY stage=bootstrap reason=internal_failure\n"
	}
	stage := StageBootstrap
	switch f.Stage {
	case StageBootstrap, StageRecovery, StageManifest, StageIdentity,
		StageInputs, StageClassifier, StageVerifier, StagePublish:
		stage = f.Stage
	}
	reason := "trust_denied"
	switch f.Kind {
	case FailureUsage:
		reason = "invalid_arguments"
	case FailureInternal:
		reason = "internal_failure"
	case FailureIO:
		reason = "io_failure"
	case FailureClassifier:
		reason = "classifier_denied"
	case FailureVerifier:
		reason = "verifier_denied"
	case FailureInterrupted:
		reason = "interrupted"
	}
	return fmt.Sprintf("root_runner=DENY stage=%s reason=%s\n", stage, reason)
}
