//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/releasejournal"
	"golang.org/x/sys/unix"
)

// VerificationSession owns every descriptor used by the currently integrated
// read-only contract chain, the exact retained execution artifact set, and the
// retained v2 release-journal session. It is intentionally not an execution
// authority: the FD-only artifact consumer and crash-recoverable
// ledger+journal admission remain disabled.
type VerificationSession struct {
	state *verificationSessionState
}

type verificationSessionState struct {
	mu               sync.Mutex
	bundle           VerifiedBundle
	journalReceipt   releasejournal.Receipt
	verificationTime time.Time
	clock            func() time.Time
	journal          *releasejournal.Session
	artifacts        *RetainedArtifactSet
	ledgerRoot       *os.File
	retained         []*os.File
	closed           bool
}

func OpenVerificationSession(ctx context.Context, command Command, roots ca42authority.RootKeyset, now time.Time) (*VerificationSession, error) {
	if ctx == nil {
		return nil, errors.New("nil root runner context")
	}
	if _, err := ParseCLI([]string{"execute", "--attempt-id", command.AttemptID}); err != nil {
		return nil, err
	}
	if unix.Geteuid() != 0 {
		return nil, errors.New("root runner requires effective UID 0")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session := &VerificationSession{state: &verificationSessionState{clock: time.Now}}
	failed := true
	defer func() {
		if failed {
			_ = session.Close()
		}
	}()
	retain := func(file *os.File) *os.File {
		session.state.retained = append(session.state.retained, file)
		return file
	}

	trustRoot, err := openFixedTrustedDirectory(TrustRootPath)
	if err != nil {
		return nil, err
	}
	retain(trustRoot)
	authorityFile, authorityBytes, _, err := openRootOwnedRegularAt(int(trustRoot.Fd()), AuthorityPath, ca42authority.MaxDescriptorBytes, nil)
	if err != nil {
		return nil, err
	}
	retain(authorityFile)
	hostFile, _, hostIdentity, err := openRootOwnedRegularAt(int(trustRoot.Fd()), HostIdentityPath, maxHostIdentityBytes, nil)
	if err != nil {
		return nil, err
	}
	retain(hostFile)
	runnerFile, runnerSHA, err := openRunningExecutable()
	if err != nil {
		return nil, err
	}
	retain(runnerFile)
	preliminary, err := ca42authority.ParseAndVerify(authorityBytes, roots, "", hostIdentity, now)
	if err != nil {
		return nil, err
	}
	if preliminary.AttemptID != command.AttemptID || preliminary.RootRunnerSHA256 != runnerSHA {
		return nil, errors.New("root runner preliminary identity binding mismatch")
	}

	incomingRoot, err := openFixedTrustedDirectory(IncomingRootPath)
	if err != nil {
		return nil, err
	}
	retain(incomingRoot)
	attemptRoot, err := openTrustedChildDirectory(int(incomingRoot.Fd()), command.AttemptID)
	if err != nil {
		return nil, err
	}
	retain(attemptRoot)
	releaseFile, releaseBytes, _, err := openRootOwnedRegularAt(int(attemptRoot.Fd()), ReleaseManifestName, ca42releaseMaxBytes(), &preliminary.ReleaseManifestSHA256)
	if err != nil {
		return nil, err
	}
	retain(releaseFile)
	executionFile, executionBytes, _, err := openRootOwnedRegularAt(int(attemptRoot.Fd()), ExecutionPlanName, ca42executionMaxBytes(), nil)
	if err != nil {
		return nil, err
	}
	retain(executionFile)
	capsuleFile, capsuleBytes, _, err := openRootOwnedRegularAt(int(attemptRoot.Fd()), TrustCapsuleName, ca42capsuleMaxBytes(), nil)
	if err != nil {
		return nil, err
	}
	retain(capsuleFile)
	externalFile, externalBytes, _, err := openRootOwnedRegularAt(int(attemptRoot.Fd()), ExternalManifestName, maxExternalBytes, nil)
	if err != nil {
		return nil, err
	}
	retain(externalFile)

	ledgerRoot, err := openFixedTrustedDirectory(LedgerRootPath)
	if err != nil {
		return nil, err
	}
	retain(ledgerRoot)
	session.state.ledgerRoot = ledgerRoot
	var previous *ca42authority.Ledger
	ledgerBytes, _, ledgerErr := readRootOwnedRegularAt(int(ledgerRoot.Fd()), LedgerPath, ca42authority.MaxLedgerBytes, nil)
	if ledgerErr == nil {
		ledger, parseErr := ca42authority.ParseLedger(ledgerBytes)
		if parseErr != nil {
			return nil, parseErr
		}
		previous = &ledger
	} else if !errors.Is(ledgerErr, unix.ENOENT) {
		return nil, ledgerErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bundle, err := VerifyBundle(authorityBytes, releaseBytes, executionBytes, capsuleBytes, externalBytes, roots, preliminary.Architecture,
		hostIdentity, runnerSHA, command.AttemptID, previous, now)
	if err != nil {
		return nil, err
	}
	artifacts, err := openRetainedArtifactSet(ctx, attemptRoot, command.AttemptID, bundle.Execution, bundle.Capsule, capsuleFile, externalFile, now)
	if err != nil {
		return nil, err
	}
	session.state.artifacts = artifacts

	journalRoot, err := openFixedTrustedDirectory(ReleaseJournalRootPath)
	if err != nil {
		return nil, err
	}
	retain(journalRoot)
	journal, err := releasejournal.OpenV2Session(journalRoot, command.AttemptID)
	if err != nil {
		return nil, err
	}
	session.state.journal = journal
	receipt, err := journal.InspectAdmissionBoundary()
	if err != nil {
		return nil, err
	}
	if err := BindAdmissionJournal(bundle, receipt); err != nil {
		return nil, err
	}
	completionTime := session.state.clock().UTC()
	if err := validateAdmissionTimeWindow(bundle, now.UTC(), completionTime); err != nil {
		return nil, err
	}
	if err := artifacts.ValidateAt(completionTime); err != nil {
		return nil, err
	}
	session.state.bundle, session.state.journalReceipt, session.state.verificationTime = bundle, receipt, now.UTC()
	failed = false
	return session, nil
}

// VerifyRetainedArtifacts proves that every retained descriptor, its fixed
// basename, exact metadata, content digest and required path-chain still match
// the verified execution plan. It grants no execution or admission authority.
func (s *VerificationSession) VerifyRetainedArtifacts(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil retained-artifact context")
	}
	if s == nil || s.state == nil {
		return errors.New("CA42 verification session closed")
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.artifacts == nil {
		return errors.New("CA42 verification session closed")
	}
	if err := state.artifacts.Revalidate(ctx); err != nil {
		return err
	}
	if state.clock == nil {
		return errors.New("CA42 trusted clock unavailable")
	}
	return state.artifacts.ValidateAt(state.clock().UTC())
}

func (s *VerificationSession) Bundle() (VerifiedBundle, error) {
	if s == nil || s.state == nil {
		return VerifiedBundle{}, errors.New("CA42 verification session closed")
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return VerifiedBundle{}, errors.New("CA42 verification session closed")
	}
	return cloneVerifiedBundle(state.bundle), nil
}

func cloneVerifiedBundle(bundle VerifiedBundle) VerifiedBundle {
	clone := bundle
	if bundle.Authority.ReleaseSignerKey != nil {
		clone.Authority.ReleaseSignerKey = append([]byte(nil), bundle.Authority.ReleaseSignerKey...)
	}
	if bundle.PreviousLedger != nil {
		previous := *bundle.PreviousLedger
		clone.PreviousLedger = &previous
	}
	return clone
}

func (s *VerificationSession) JournalReceipt() (releasejournal.Receipt, error) {
	if s == nil || s.state == nil {
		return releasejournal.Receipt{}, errors.New("CA42 verification session closed")
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.journal == nil {
		return releasejournal.Receipt{}, errors.New("CA42 verification session closed")
	}
	current, err := state.journal.InspectAdmissionBoundary()
	if err != nil {
		return releasejournal.Receipt{}, err
	}
	if current != state.journalReceipt {
		return releasejournal.Receipt{}, errors.New("CA42 retained journal receipt changed")
	}
	return current, nil
}

// ReserveAndAdvanceAdmission is deliberately fail-closed until the CA42
// capture/consumer contract can retain the exact artifacts that will be used.
// In particular, the current preflight creates new dumps after signing and the
// current pathtrust helper reopens the attestation core by pathname. Neither
// behavior may consume the authority ledger or advance the release journal.
func (s *VerificationSession) ReserveAndAdvanceAdmission(ctx context.Context, _ *os.File) error {
	if ctx == nil {
		return errors.New("nil admission context")
	}
	if s == nil || s.state == nil {
		return errors.New("CA42 verification session closed")
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.journal == nil || state.ledgerRoot == nil {
		return errors.New("CA42 verification session closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state.clock == nil {
		return errors.New("CA42 trusted clock unavailable")
	}
	commitTime := state.clock().UTC()
	if err := validateAdmissionTimeWindow(state.bundle, state.verificationTime, commitTime); err != nil {
		return err
	}
	return errors.New("CA42 admission disabled: retained artifact consumer contract incomplete")
}

func (s *VerificationSession) validateAdmissionTime(now time.Time) error {
	if s == nil || s.state == nil {
		return errors.New("CA42 verification session closed")
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	return validateAdmissionTimeWindow(s.state.bundle, s.state.verificationTime, now)
}

func (s *VerificationSession) Close() error {
	if s == nil || s.state == nil {
		return nil
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	var result error
	if state.journal != nil {
		result = errors.Join(result, state.journal.Close())
		state.journal = nil
	}
	if state.artifacts != nil {
		result = errors.Join(result, state.artifacts.Close())
		state.artifacts = nil
	}
	for index := len(state.retained) - 1; index >= 0; index-- {
		if state.retained[index] != nil {
			result = errors.Join(result, state.retained[index].Close())
		}
	}
	state.retained = nil
	return result
}
