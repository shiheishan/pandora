//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	exitUsage  = 64
	exitDenied = 78
	exitSystem = 70

	linuxSYSOpenat2     = 437
	linuxONoFollow      = 0x20000
	linuxOCloExec       = 0x80000
	linuxODirectory     = 0x10000
	resolveNoMagicLinks = 0x02
	resolveNoSymlinks   = 0x04
	resolveBeneath      = 0x08
	renameNoReplace     = 1
)

var (
	releaseWrite     = syscall.Write
	releaseFdatasync = syscall.Fdatasync
	releaseFsync     = syscall.Fsync
	releaseRename    = renameAt2NoReplace
	journalStageRE   = regexp.MustCompile(`^\.pandora-release-journal\.[0-9a-f]{32}\.stage$`)
	recordStageRE    = regexp.MustCompile(`^\.pandora-release-record\.[0-9a-f]{64}\.stage$`)
	journalTempRE    = regexp.MustCompile(`^\.pandora-release-journal\.[0-9a-f]{32}\.tmp$`)
	recordTempRE     = regexp.MustCompile(`^\.pandora-release-record\.[0-9a-f]{32}\.tmp$`)
)

type openHow struct{ Flags, Mode, Resolve uint64 }
type usageError struct{ reason string }

func (e *usageError) Error() string { return e.reason }

type policyError struct {
	reason string
	err    error
}

func (e *policyError) Error() string {
	if e.err == nil {
		return e.reason
	}
	return e.reason + ": " + e.err.Error()
}
func (e *policyError) Unwrap() error         { return e.err }
func usage(reason string) error              { return &usageError{reason} }
func deny(reason string) error               { return &policyError{reason: reason} }
func denyErr(reason string, err error) error { return &policyError{reason: reason, err: err} }

type pathPolicy struct {
	root           string
	expectedDevice uint64
	allowedDevices map[uint64]struct{}
}

// RunCLI preserves the recovery CLI while keeping storage and durability in
// the importable releasejournal package used by the CA42 root runner.
func RunCLI(args []string) int {
	if len(args) == 0 {
		printUsage()
		return exitUsage
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "pandora-release-journal: trust denied: euid_zero_required")
		return exitDenied
	}
	var err error
	switch args[0] {
	case "prepare":
		err = prepareCommand(args[1:])
	case "inspect":
		err = inspectCommand(args[1:])
	case "advance":
		err = advanceCommand(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		err = usage("unknown_command")
	}
	if err == nil {
		return 0
	}
	var denied *policyError
	if errors.As(err, &denied) {
		fmt.Fprintf(os.Stderr, "pandora-release-journal: trust denied: %s\n", denied)
		return exitDenied
	}
	var badUsage *usageError
	if errors.As(err, &badUsage) {
		fmt.Fprintf(os.Stderr, "pandora-release-journal: usage: %s\n", badUsage)
		return exitUsage
	}
	fmt.Fprintf(os.Stderr, "pandora-release-journal: system failure: %v\n", err)
	return exitSystem
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: pandora-release-journal prepare|inspect|advance [flags]")
	fmt.Fprintln(os.Stderr, "all commands require EUID 0 and trusted --journal-root path identity flags")
}

func addPathFlags(fs *flag.FlagSet) (*string, *string, *string) {
	return fs.String("journal-root", "", "absolute trusted journal root"),
		fs.String("expected-root-device", "", "exact decimal st_dev"),
		fs.String("allow-devices", "", "strict sorted comma-separated st_dev allowlist")
}

func parsePathPolicy(root, expected, allow string) (pathPolicy, error) {
	if root == "" || expected == "" || allow == "" {
		return pathPolicy{}, usage("path_identity_flag_missing")
	}
	expectedDevice, err := parsePositiveUint(expected)
	if err != nil {
		return pathPolicy{}, deny("expected_root_device_invalid")
	}
	allowed, err := parseAllowedDevices(allow)
	if err != nil {
		return pathPolicy{}, deny("allowed_devices_invalid")
	}
	if _, ok := allowed[expectedDevice]; !ok {
		return pathPolicy{}, deny("expected_root_device_not_allowed")
	}
	return pathPolicy{root: root, expectedDevice: expectedDevice, allowedDevices: allowed}, nil
}

func parsePositiveUint(value string) (uint64, error) {
	if !canonicalPositive(value) {
		return 0, errors.New("not_canonical_positive")
	}
	return strconv.ParseUint(value, 10, 64)
}

func parseAllowedDevices(value string) (map[uint64]struct{}, error) {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > 32 {
		return nil, errors.New("device_count_invalid")
	}
	result := make(map[uint64]struct{}, len(parts))
	var previous uint64
	for index, part := range parts {
		device, err := parsePositiveUint(part)
		if err != nil || (index > 0 && device <= previous) {
			return nil, errors.New("device_order_invalid")
		}
		result[device] = struct{}{}
		previous = device
	}
	return result, nil
}

func prepareCommand(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, rootDev, devices := addPathFlags(fs)
	identity := preparedIdentity{}
	fs.StringVar(&identity.ReleaseAttemptID, "attempt-id", "", "safe immutable attempt id")
	fs.StringVar(&identity.CreatedAtEpoch, "created-at-epoch", "", "canonical epoch")
	fs.StringVar(&identity.ReleaseID, "release-id", "", "safe release id")
	fs.StringVar(&identity.Architecture, "architecture", "", "amd64 or arm64")
	fs.StringVar(&identity.ControllerSHA256, "controller-sha256", "", "controller hash")
	fs.StringVar(&identity.ReleaseManifestSHA256, "release-manifest-sha256", "", "release manifest hash")
	fs.StringVar(&identity.TargetIdentitySHA256, "target-identity-sha256", "", "target identity hash")
	fs.StringVar(&identity.TargetRootDevice, "target-root-device", "", "target root st_dev")
	fs.StringVar(&identity.TargetRootInode, "target-root-inode", "", "target root st_ino")
	fs.StringVar(&identity.StagedTreeManifestSHA256, "staged-tree-manifest-sha256", "", "staged tree hash")
	fs.StringVar(&identity.LiveTreeManifestSHA256, "live-tree-manifest-sha256", "", "live tree hash")
	fs.StringVar(&identity.EnvironmentFileSHA256, "environment-file-sha256", "", "environment hash")
	fs.StringVar(&identity.BackupControllerSHA256, "backup-controller-sha256", "", "backup controller hash")
	fs.StringVar(&identity.MachineIdentitySHA256, "machine-identity-sha256", "", "machine identity hash")
	fs.StringVar(&identity.BootIDSHA256, "boot-id-sha256", "", "boot id hash")
	fs.StringVar(&identity.PostgresSystemIdentifier, "postgres-system-identifier", "", "Postgres system id")
	fs.StringVar(&identity.DatabaseName, "database-name", "", "database name")
	fs.StringVar(&identity.DatabaseOID, "database-oid", "", "database oid")
	fs.StringVar(&identity.SourceWaterline, "source-waterline", "", "source migration waterline")
	fs.StringVar(&identity.AuthorizedTargetWaterline, "authorized-target-waterline", "", "authorized target waterline")
	fs.StringVar(&identity.MigrationSetSHA256, "migration-set-sha256", "", "migration set hash")
	fs.StringVar(&identity.IngressUnitSHA256, "ingress-unit-sha256", "", "ingress unit hash")
	fs.StringVar(&identity.WriterUnitsSHA256, "writer-units-sha256", "", "writer units hash")
	fs.StringVar(&identity.HealthConfigSHA256, "health-config-sha256", "", "health config hash")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("prepare_flags_invalid")
	}
	policy, err := parsePathPolicy(*root, *rootDev, *devices)
	if err != nil {
		return err
	}
	identity.JournalID = strings.Repeat("0", 64)
	if err := validatePreparedIdentity(identity); err != nil {
		return denyErr("prepared_identity_invalid", err)
	}
	return prepareJournal(policy, identity)
}

func inspectCommand(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, rootDev, devices := addPathFlags(fs)
	attempt := fs.String("attempt-id", "", "safe immutable attempt id")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("inspect_flags_invalid")
	}
	if !safeTokenRE.MatchString(*attempt) {
		return deny("release_attempt_id_invalid")
	}
	policy, err := parsePathPolicy(*root, *rootDev, *devices)
	if err != nil {
		return err
	}
	rootFD, rootStat, err := openTrustedRoot(policy)
	if err != nil {
		return err
	}
	defer syscall.Close(rootFD)
	if err := syscall.Flock(rootFD, syscall.LOCK_SH); err != nil {
		return fmt.Errorf("lock root: %w", err)
	}
	defer syscall.Flock(rootFD, syscall.LOCK_UN)
	name, err := discoverJournal(rootFD, *attempt, policy.allowedDevices)
	if err != nil {
		return err
	}
	journalFD, journalStat, err := openJournalDirectory(rootFD, name, uint64(rootStat.Dev), policy.allowedDevices)
	if err != nil {
		return err
	}
	defer syscall.Close(journalFD)
	if err := syscall.Flock(journalFD, syscall.LOCK_SH); err != nil {
		return fmt.Errorf("lock journal: %w", err)
	}
	defer syscall.Flock(journalFD, syscall.LOCK_UN)
	snapshot, err := loadSnapshot(journalFD, uint64(journalStat.Dev))
	if err != nil {
		return err
	}
	if err := validateJournalName(name, snapshot); err != nil {
		return err
	}
	if err := validateDirectoryIdentity(rootFD, rootStat, policy.allowedDevices); err != nil {
		return err
	}
	printSnapshot(snapshot, name, false)
	return nil
}

func advanceCommand(args []string) error {
	fs := flag.NewFlagSet("advance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, rootDev, devices := addPathFlags(fs)
	attempt := fs.String("attempt-id", "", "attempt id")
	expected := fs.String("expect-journal-sha256", "", "CAS manifest")
	from := fs.String("from", "", "expected current state")
	to := fs.String("to", "", "next state")
	EventID := fs.String("event-id", "", "deterministic event id hash")
	occurred := fs.String("occurred-at-epoch", "", "canonical epoch")
	evidenceFDValue := fs.String("evidence-fd", "", "inherited evidence file descriptor")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("advance_flags_invalid")
	}
	if !safeTokenRE.MatchString(*attempt) || !hex64RE.MatchString(*expected) || !hex64RE.MatchString(*EventID) || !canonicalPositive(*occurred) {
		return deny("advance_identity_invalid")
	}
	fromState, toState := releaseState(*from), releaseState(*to)
	if _, ok := stateSegment[fromState]; !ok {
		return deny("from_state_invalid")
	}
	if _, ok := stateSegment[toState]; !ok {
		return deny("to_state_invalid")
	}
	if err := validateTransition(fromState, toState); err != nil {
		return denyErr("transition_invalid", err)
	}
	evidenceFD64, err := parsePositiveUint(*evidenceFDValue)
	if err != nil || evidenceFD64 > uint64(^uint(0)>>1) {
		return deny("evidence_fd_invalid")
	}
	policy, err := parsePathPolicy(*root, *rootDev, *devices)
	if err != nil {
		return err
	}
	evidence, err := readEvidenceFD(int(evidenceFD64), policy.allowedDevices)
	if err != nil {
		return err
	}
	return advanceJournal(policy, *attempt, *expected, fromState, toState, *EventID, *occurred, evidence)
}

type evidenceDigest struct {
	sha256 string
	size   int64
}

func readEvidenceFD(fd int, allowed map[uint64]struct{}) (evidenceDigest, error) {
	if fd < 3 {
		return evidenceDigest{}, deny("evidence_fd_must_be_inherited")
	}
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return evidenceDigest{}, denyErr("evidence_fstat_failed", err)
	}
	if err := validateImmutableFile(before, uint64(before.Dev), allowed); err != nil {
		return evidenceDigest{}, err
	}
	if before.Size <= 0 || before.Size > maxRecordBytes {
		return evidenceDigest{}, deny("evidence_size_invalid")
	}
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return evidenceDigest{}, fmt.Errorf("dup evidence: %w", err)
	}
	file := os.NewFile(uintptr(duplicate), "release-evidence")
	if file == nil {
		syscall.Close(duplicate)
		return evidenceDigest{}, errors.New("wrap evidence fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return evidenceDigest{}, fmt.Errorf("seek evidence: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return evidenceDigest{}, fmt.Errorf("read evidence: %w", err)
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return evidenceDigest{}, fmt.Errorf("fstat evidence after: %w", err)
	}
	if !sameFileSnapshot(before, after) || int64(len(data)) != after.Size {
		return evidenceDigest{}, deny("evidence_changed_or_short_read")
	}
	return evidenceDigest{sha256: sha256Bytes(data), size: after.Size}, nil
}

func prepareJournal(policy pathPolicy, requested preparedIdentity) error {
	rootFD, rootStat, err := openTrustedRoot(policy)
	if err != nil {
		return err
	}
	defer syscall.Close(rootFD)
	if err := syscall.Flock(rootFD, syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock root: %w", err)
	}
	defer syscall.Flock(rootFD, syscall.LOCK_UN)
	existing, discoverErr := discoverJournal(rootFD, requested.ReleaseAttemptID, policy.allowedDevices)
	if discoverErr == nil {
		journalFD, journalStat, err := openJournalDirectory(rootFD, existing, uint64(rootStat.Dev), policy.allowedDevices)
		if err != nil {
			return err
		}
		defer syscall.Close(journalFD)
		if err := syscall.Flock(journalFD, syscall.LOCK_EX); err != nil {
			return fmt.Errorf("lock existing journal: %w", err)
		}
		defer syscall.Flock(journalFD, syscall.LOCK_UN)
		snapshot, err := loadSnapshot(journalFD, uint64(journalStat.Dev))
		if err != nil {
			return err
		}
		if !samePreparedRequest(snapshot.Prepared, requested) {
			return deny("release_attempt_identity_conflict")
		}
		if err := validateJournalName(existing, snapshot); err != nil {
			return err
		}
		if err := syncSnapshot(journalFD, uint64(journalStat.Dev), snapshot); err != nil {
			return err
		}
		if err := releaseFsync(rootFD); err != nil {
			return fmt.Errorf("fsync root recovery: %w", err)
		}
		printSnapshot(snapshot, existing, true)
		return nil
	}
	var denied *policyError
	if !errors.As(discoverErr, &denied) || denied.reason != "release_journal_not_found" {
		return discoverErr
	}
	stageID := journalStageID(requested.JournalFormat, requested.ReleaseAttemptID)
	stageName := ".pandora-release-journal." + stageID + ".stage"
	stageFD, stageStat, err := openJournalDirectory(rootFD, stageName, uint64(rootStat.Dev), policy.allowedDevices)
	if err != nil {
		names, listErr := listDirectoryNames(rootFD)
		if listErr != nil {
			return listErr
		}
		for _, name := range names {
			if name == stageName {
				return fmt.Errorf("open existing prepared stage: %w", err)
			}
		}
		tempID, randomErr := randomHex(16)
		if randomErr != nil {
			return randomErr
		}
		tempName := ".pandora-release-journal." + tempID + ".tmp"
		if err := mkdirAt(rootFD, tempName, 0700); err != nil {
			return fmt.Errorf("mkdir prepare temp: %w", err)
		}
		tempFD, tempStat, openErr := openJournalDirectory(rootFD, tempName, uint64(rootStat.Dev), policy.allowedDevices)
		if openErr != nil {
			return openErr
		}
		JournalID, journalErr := randomHex(32)
		if journalErr != nil {
			syscall.Close(tempFD)
			return fmt.Errorf("random journal id: %w", journalErr)
		}
		requested.JournalID = JournalID
		record, _, recordErr := preparedRecordBytes(requested)
		if recordErr != nil {
			syscall.Close(tempFD)
			return denyErr("prepared_record_invalid", recordErr)
		}
		if err := writeImmutableAt(tempFD, stateSegment[statePrepared], record, uint64(tempStat.Dev)); err != nil {
			syscall.Close(tempFD)
			return err
		}
		if err := releaseFsync(tempFD); err != nil {
			syscall.Close(tempFD)
			return fmt.Errorf("fsync prepare temp: %w", err)
		}
		if err := releaseRename(rootFD, tempName, rootFD, stageName); err != nil {
			syscall.Close(tempFD)
			return fmt.Errorf("publish prepare stage: %w", err)
		}
		if err := releaseFsync(rootFD); err != nil {
			syscall.Close(tempFD)
			return fmt.Errorf("fsync prepare stage root: %w", err)
		}
		// rename preserves the directory inode. Retain the exact open
		// description instead of resolving the stage basename again.
		stageFD, stageStat, err = tempFD, tempStat, nil
	}
	if err != nil {
		return fmt.Errorf("open prepared stage: %w", err)
	}
	defer syscall.Close(stageFD)
	staged, err := loadSnapshot(stageFD, uint64(stageStat.Dev))
	if err != nil {
		return denyErr("prepared_stage_unrecoverable", err)
	}
	if staged.State != statePrepared || !samePreparedRequest(staged.Prepared, requested) {
		return deny("prepared_stage_identity_conflict")
	}
	if err := syncSnapshot(stageFD, uint64(stageStat.Dev), staged); err != nil {
		return fmt.Errorf("resync prepared stage: %w", err)
	}
	requested.JournalID = staged.Prepared.JournalID
	finalName := requested.ReleaseAttemptID + "." + requested.JournalID + ".release-journal"
	if err := releaseFsync(stageFD); err != nil {
		return fmt.Errorf("fsync stage directory: %w", err)
	}
	if err := releaseRename(rootFD, stageName, rootFD, finalName); err != nil {
		return fmt.Errorf("publish journal: %w", err)
	}
	if err := releaseFsync(rootFD); err != nil {
		return fmt.Errorf("fsync journal root: %w", err)
	}
	journalFD, journalStat, err := openJournalDirectory(rootFD, finalName, uint64(rootStat.Dev), policy.allowedDevices)
	if err != nil {
		return err
	}
	defer syscall.Close(journalFD)
	snapshot, err := loadSnapshot(journalFD, uint64(journalStat.Dev))
	if err != nil {
		return err
	}
	if !samePreparedRequest(snapshot.Prepared, requested) {
		return deny("published_prepare_identity_mismatch")
	}
	if err := validateJournalName(finalName, snapshot); err != nil {
		return err
	}
	if err := validateDirectoryIdentity(rootFD, rootStat, policy.allowedDevices); err != nil {
		return err
	}
	printSnapshot(snapshot, finalName, false)
	return nil
}

func samePreparedRequest(existing, requested preparedIdentity) bool {
	existing.JournalID = ""
	requested.JournalID = ""
	if existing.JournalFormat == "" {
		existing.JournalFormat = releaseJournalFormat
	}
	if requested.JournalFormat == "" {
		requested.JournalFormat = releaseJournalFormat
	}
	return reflect.DeepEqual(existing, requested)
}

func advanceJournal(policy pathPolicy, attempt, expected string, from, to releaseState, EventID, occurred string, evidence evidenceDigest) error {
	rootFD, rootStat, err := openTrustedRoot(policy)
	if err != nil {
		return err
	}
	defer syscall.Close(rootFD)
	if err := syscall.Flock(rootFD, syscall.LOCK_SH); err != nil {
		return fmt.Errorf("lock root: %w", err)
	}
	defer syscall.Flock(rootFD, syscall.LOCK_UN)
	name, err := discoverJournal(rootFD, attempt, policy.allowedDevices)
	if err != nil {
		return err
	}
	journalFD, journalStat, err := openJournalDirectory(rootFD, name, uint64(rootStat.Dev), policy.allowedDevices)
	if err != nil {
		return err
	}
	defer syscall.Close(journalFD)
	if err := syscall.Flock(journalFD, syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock journal: %w", err)
	}
	defer syscall.Flock(journalFD, syscall.LOCK_UN)
	updated, recovered, err := advanceOpenJournal(rootFD, journalFD, rootStat, journalStat, name, policy, attempt, expected, from, to, EventID, occurred, evidence)
	if err != nil {
		return err
	}
	printSnapshot(updated, name, recovered)
	return nil
}

func advanceOpenJournal(rootFD, journalFD int, rootStat, journalStat syscall.Stat_t, journalName string, policy pathPolicy, attempt, expected string, from, to releaseState, EventID, occurred string, evidence evidenceDigest) (releaseJournalSnapshot, bool, error) {
	snapshot, err := loadSnapshot(journalFD, uint64(journalStat.Dev))
	if err != nil {
		return releaseJournalSnapshot{}, false, err
	}
	if err := validateJournalName(journalName, snapshot); err != nil {
		return releaseJournalSnapshot{}, false, err
	}
	record := transitionRecord{JournalFormat: snapshot.JournalFormat, JournalID: snapshot.Prepared.JournalID, ReleaseAttemptID: attempt, Sequence: strconv.Itoa(len(snapshot.Names)), EventID: EventID, OccurredAtEpoch: occurred, PreviousState: from, State: to, PreviousRecordSHA256: snapshot.HeadSHA256, EvidenceSHA256: evidence.sha256, EvidenceSize: strconv.FormatInt(evidence.size, 10)}
	if snapshot.ManifestSHA != expected || snapshot.State != from {
		recovered, recoveryErr := recoverAdvance(rootFD, journalFD, snapshot, expected, record)
		return recovered, true, recoveryErr
	}
	priorEpoch := snapshot.Prepared.CreatedAtEpoch
	if len(snapshot.Transitions) > 0 {
		priorEpoch = snapshot.Transitions[len(snapshot.Transitions)-1].OccurredAtEpoch
	}
	if compareCanonicalUint(occurred, priorEpoch) < 0 {
		return releaseJournalSnapshot{}, false, deny("transition_time_not_monotonic")
	}
	data, _, err := transitionRecordBytes(record)
	if err != nil {
		return releaseJournalSnapshot{}, false, denyErr("transition_record_invalid", err)
	}
	stageName := transitionStageName(snapshot.JournalFormat, attempt, EventID)
	stageFD, _, stageErr := openImmutableAt(rootFD, stageName, policy.expectedDevice, policy.allowedDevices)
	if stageErr != nil && errors.Is(stageErr, syscall.ENOENT) {
		tempID, randomErr := randomHex(16)
		if randomErr != nil {
			return releaseJournalSnapshot{}, false, randomErr
		}
		tempName := ".pandora-release-record." + tempID + ".tmp"
		if err := writeImmutableAt(rootFD, tempName, data, policy.expectedDevice); err != nil {
			return releaseJournalSnapshot{}, false, err
		}
		if err := releaseFsync(rootFD); err != nil {
			return releaseJournalSnapshot{}, false, fmt.Errorf("fsync transition temp root: %w", err)
		}
		if err := releaseRename(rootFD, tempName, rootFD, stageName); err != nil {
			return releaseJournalSnapshot{}, false, fmt.Errorf("publish transition stage: %w", err)
		}
		if err := releaseFsync(rootFD); err != nil {
			return releaseJournalSnapshot{}, false, fmt.Errorf("fsync transition stage root: %w", err)
		}
		stageFD, _, stageErr = openImmutableAt(rootFD, stageName, policy.expectedDevice, policy.allowedDevices)
	}
	if stageErr != nil {
		return releaseJournalSnapshot{}, false, stageErr
	}
	stagedBytes, _, readErr := readStableFD(stageFD)
	syscall.Close(stageFD)
	if readErr != nil {
		return releaseJournalSnapshot{}, false, readErr
	}
	if !reflect.DeepEqual(stagedBytes, data) {
		return releaseJournalSnapshot{}, false, deny("transition_stage_identity_conflict")
	}
	stageFD, _, stageErr = openImmutableAt(rootFD, stageName, policy.expectedDevice, policy.allowedDevices)
	if stageErr != nil {
		return releaseJournalSnapshot{}, false, stageErr
	}
	if err := releaseFdatasync(stageFD); err != nil {
		syscall.Close(stageFD)
		return releaseJournalSnapshot{}, false, fmt.Errorf("fdatasync transition stage: %w", err)
	}
	if err := releaseFsync(stageFD); err != nil {
		syscall.Close(stageFD)
		return releaseJournalSnapshot{}, false, fmt.Errorf("fsync transition stage: %w", err)
	}
	resyncedBytes, _, readErr := readStableFD(stageFD)
	syscall.Close(stageFD)
	if readErr != nil {
		return releaseJournalSnapshot{}, false, readErr
	}
	if !reflect.DeepEqual(resyncedBytes, data) {
		return releaseJournalSnapshot{}, false, deny("transition_stage_changed_after_sync")
	}
	if err := releaseFsync(rootFD); err != nil {
		return releaseJournalSnapshot{}, false, fmt.Errorf("fsync staged record root: %w", err)
	}
	if err := releaseRename(rootFD, stageName, journalFD, stateSegment[to]); err != nil {
		return releaseJournalSnapshot{}, false, fmt.Errorf("publish transition: %w", err)
	}
	if err := releaseFsync(journalFD); err != nil {
		return releaseJournalSnapshot{}, false, fmt.Errorf("fsync journal after transition: %w", err)
	}
	if err := releaseFsync(rootFD); err != nil {
		return releaseJournalSnapshot{}, false, fmt.Errorf("fsync root after transition: %w", err)
	}
	updated, err := loadSnapshot(journalFD, uint64(journalStat.Dev))
	if err != nil {
		return releaseJournalSnapshot{}, false, err
	}
	if updated.State != to || len(updated.Names) != len(snapshot.Names)+1 {
		return releaseJournalSnapshot{}, false, deny("published_transition_mismatch")
	}
	if err := validateDirectoryIdentity(rootFD, rootStat, policy.allowedDevices); err != nil {
		return releaseJournalSnapshot{}, false, err
	}
	if err := validateDirectoryIdentity(journalFD, journalStat, policy.allowedDevices); err != nil {
		return releaseJournalSnapshot{}, false, err
	}
	return updated, false, nil
}

func journalStageID(format, attempt string) string {
	domain := "pandora-release-journal-stage-v1\n"
	if format == releaseJournalFormatV2 {
		domain = "pandora-release-journal-stage-v2\n"
	} else if format == FormatV3 {
		domain = "pandora-release-journal-stage-v3\n"
	}
	return sha256Bytes([]byte(domain + attempt))[:32]
}

func transitionStageName(format, attempt, EventID string) string {
	domain := "pandora-release-record-stage-v1\n"
	if format == releaseJournalFormatV2 {
		domain = "pandora-release-record-stage-v2\n"
	} else if format == FormatV3 {
		domain = "pandora-release-record-stage-v3\n"
	}
	key := sha256Bytes([]byte(domain + attempt + "\n" + EventID))
	return ".pandora-release-record." + key + ".stage"
}

func recoverAdvance(rootFD, journalFD int, current releaseJournalSnapshot, expected string, requested transitionRecord) (releaseJournalSnapshot, error) {
	if len(current.Names) < 2 || current.State != requested.State {
		return releaseJournalSnapshot{}, deny("journal_cas_or_state_conflict")
	}
	prefix, err := snapshotPrefix(current, len(current.Names)-1)
	if err != nil {
		return releaseJournalSnapshot{}, denyErr("recovery_prefix_invalid", err)
	}
	if prefix.ManifestSHA != expected || prefix.State != requested.PreviousState {
		return releaseJournalSnapshot{}, deny("journal_cas_or_state_conflict")
	}
	priorEpoch := prefix.Prepared.CreatedAtEpoch
	if len(prefix.Transitions) > 0 {
		priorEpoch = prefix.Transitions[len(prefix.Transitions)-1].OccurredAtEpoch
	}
	if compareCanonicalUint(requested.OccurredAtEpoch, priorEpoch) < 0 {
		return releaseJournalSnapshot{}, deny("transition_time_not_monotonic")
	}
	requested.PreviousRecordSHA256 = prefix.HeadSHA256
	requested.Sequence = strconv.Itoa(len(prefix.Names))
	expectedBytes, _, err := transitionRecordBytes(requested)
	if err != nil {
		return releaseJournalSnapshot{}, denyErr("recovery_record_invalid", err)
	}
	if !reflect.DeepEqual(expectedBytes, current.RecordBytes[len(current.RecordBytes)-1]) {
		return releaseJournalSnapshot{}, deny("exact_retry_record_mismatch")
	}
	var journalStat syscall.Stat_t
	if err := syscall.Fstat(journalFD, &journalStat); err != nil {
		return releaseJournalSnapshot{}, err
	}
	lastFD, _, err := openImmutableAt(journalFD, current.Names[len(current.Names)-1], uint64(journalStat.Dev), nil)
	if err != nil {
		return releaseJournalSnapshot{}, err
	}
	actualBytes, _, err := readStableFD(lastFD)
	if err != nil {
		syscall.Close(lastFD)
		return releaseJournalSnapshot{}, err
	}
	if !reflect.DeepEqual(actualBytes, current.RecordBytes[len(current.RecordBytes)-1]) {
		syscall.Close(lastFD)
		return releaseJournalSnapshot{}, deny("recovered_record_changed")
	}
	if err := releaseFdatasync(lastFD); err != nil {
		syscall.Close(lastFD)
		return releaseJournalSnapshot{}, fmt.Errorf("fdatasync recovered record: %w", err)
	}
	if err := releaseFsync(lastFD); err != nil {
		syscall.Close(lastFD)
		return releaseJournalSnapshot{}, fmt.Errorf("fsync recovered record: %w", err)
	}
	syscall.Close(lastFD)
	if err := releaseFsync(journalFD); err != nil {
		return releaseJournalSnapshot{}, fmt.Errorf("fsync recovered journal: %w", err)
	}
	if err := releaseFsync(rootFD); err != nil {
		return releaseJournalSnapshot{}, fmt.Errorf("fsync recovered root: %w", err)
	}
	reloaded, err := loadSnapshot(journalFD, uint64(journalStat.Dev))
	if err != nil {
		return releaseJournalSnapshot{}, err
	}
	if reloaded.ManifestSHA != current.ManifestSHA {
		return releaseJournalSnapshot{}, deny("recovered_journal_changed")
	}
	return current, nil
}

func discoverJournal(rootFD int, attempt string, allowed map[uint64]struct{}) (string, error) {
	Names, err := listDirectoryNames(rootFD)
	if err != nil {
		return "", err
	}
	matches := []string{}
	stageCount := 0
	var rootStat syscall.Stat_t
	if err := syscall.Fstat(rootFD, &rootStat); err != nil {
		return "", err
	}
	for _, name := range Names {
		if journalStageRE.MatchString(name) || journalTempRE.MatchString(name) {
			stageCount++
			fd, _, openErr := openJournalDirectory(rootFD, name, uint64(rootStat.Dev), allowed)
			if openErr != nil {
				return "", openErr
			}
			syscall.Close(fd)
			continue
		}
		if recordStageRE.MatchString(name) || recordTempRE.MatchString(name) {
			stageCount++
			fd, _, openErr := openImmutableAt(rootFD, name, uint64(rootStat.Dev), allowed)
			if openErr != nil {
				return "", openErr
			}
			syscall.Close(fd)
			continue
		}
		parts := journalRE.FindStringSubmatch(name)
		if parts == nil {
			return "", deny("unknown_entry_in_journal_root")
		}
		if parts[1] == attempt {
			matches = append(matches, name)
		}
	}
	if stageCount > 8 {
		return "", deny("release_stage_limit_exceeded")
	}
	if len(matches) == 0 {
		return "", deny("release_journal_not_found")
	}
	if len(matches) != 1 {
		return "", deny("multiple_release_journals_for_attempt")
	}
	return matches[0], nil
}

func validateJournalName(name string, snapshot releaseJournalSnapshot) error {
	parts := journalRE.FindStringSubmatch(name)
	if parts == nil || parts[1] != snapshot.Prepared.ReleaseAttemptID || parts[2] != snapshot.Prepared.JournalID {
		return deny("journal_basename_record_binding_mismatch")
	}
	return nil
}

func loadSnapshot(journalFD int, device uint64) (releaseJournalSnapshot, error) {
	Names, err := listDirectoryNames(journalFD)
	if err != nil {
		return releaseJournalSnapshot{}, err
	}
	segments := make(map[string][]byte, len(Names))
	validNames := map[string]struct{}{}
	for _, name := range stateSegment {
		validNames[name] = struct{}{}
	}
	for _, name := range Names {
		if _, ok := validNames[name]; !ok {
			return releaseJournalSnapshot{}, deny("unknown_journal_segment")
		}
		fd, _, err := openImmutableAt(journalFD, name, device, nil)
		if err != nil {
			return releaseJournalSnapshot{}, err
		}
		data, _, err := readStableFD(fd)
		syscall.Close(fd)
		if err != nil {
			return releaseJournalSnapshot{}, err
		}
		segments[name] = data
	}
	snapshot, err := parseReleaseJournal(segments)
	if err != nil {
		return releaseJournalSnapshot{}, denyErr("journal_parse_failed", err)
	}
	return snapshot, nil
}

func printSnapshot(snapshot releaseJournalSnapshot, name string, recovered bool) {
	fmt.Println("format=pandora-release-journal-result-v1")
	if name != "" {
		fmt.Println("journal=" + name)
	}
	fmt.Println("release_attempt_id=" + snapshot.Prepared.ReleaseAttemptID)
	fmt.Println("state=" + string(snapshot.State))
	fmt.Println("sequence=" + strconv.Itoa(len(snapshot.Names)-1))
	fmt.Println("head_sha256=" + snapshot.HeadSHA256)
	fmt.Println("journal_sha256=" + snapshot.ManifestSHA)
	fmt.Println("recovered=" + strconv.FormatBool(recovered))
}

func syncSnapshot(journalFD int, device uint64, snapshot releaseJournalSnapshot) error {
	for index, name := range snapshot.Names {
		fd, _, err := openImmutableAt(journalFD, name, device, nil)
		if err != nil {
			return err
		}
		actual, _, err := readStableFD(fd)
		if err != nil {
			syscall.Close(fd)
			return err
		}
		if !reflect.DeepEqual(actual, snapshot.RecordBytes[index]) {
			syscall.Close(fd)
			return deny("snapshot_record_changed")
		}
		if err := releaseFdatasync(fd); err != nil {
			syscall.Close(fd)
			return err
		}
		if err := releaseFsync(fd); err != nil {
			syscall.Close(fd)
			return err
		}
		syscall.Close(fd)
	}
	if err := releaseFsync(journalFD); err != nil {
		return err
	}
	reloaded, err := loadSnapshot(journalFD, device)
	if err != nil {
		return err
	}
	if reloaded.ManifestSHA != snapshot.ManifestSHA {
		return deny("snapshot_manifest_changed")
	}
	return nil
}

func writeImmutableAt(dirFD int, name string, data []byte, expectedDevice uint64) error {
	if !safePathComponent(name) {
		return deny("record_name_invalid")
	}
	fd, err := openAt2(dirFD, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|linuxONoFollow|linuxOCloExec, 0600)
	if err != nil {
		return fmt.Errorf("create immutable record: %w", err)
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return err
	}
	if err := validateImmutableFile(stat, expectedDevice, nil); err != nil {
		return err
	}
	if err := writeFull(fd, data); err != nil {
		return fmt.Errorf("write immutable record: %w", err)
	}
	if err := releaseFdatasync(fd); err != nil {
		return fmt.Errorf("fdatasync immutable record: %w", err)
	}
	if err := releaseFsync(fd); err != nil {
		return fmt.Errorf("fsync immutable record: %w", err)
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return err
	}
	if !sameFileIdentity(stat, after) || after.Size != int64(len(data)) {
		return deny("immutable_record_changed")
	}
	return nil
}

func openImmutableAt(dirFD int, name string, expectedDevice uint64, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	fd, err := openAt2(dirFD, name, syscall.O_RDONLY|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, denyErr("record_open_failed", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		syscall.Close(fd)
		return -1, stat, err
	}
	if err := validateImmutableFile(stat, expectedDevice, allowed); err != nil {
		syscall.Close(fd)
		return -1, stat, err
	}
	return fd, stat, nil
}

func validateImmutableFile(stat syscall.Stat_t, expectedDevice uint64, allowed map[uint64]struct{}) error {
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return deny("record_not_regular")
	}
	if stat.Uid != 0 || stat.Mode&07777 != 0600 || stat.Nlink != 1 {
		return deny("record_identity_or_mode_invalid")
	}
	if expectedDevice != ^uint64(0) && uint64(stat.Dev) != expectedDevice {
		return deny("record_device_mismatch")
	}
	if allowed != nil {
		if _, ok := allowed[uint64(stat.Dev)]; !ok {
			return deny("record_device_not_allowed")
		}
	}
	return nil
}

func readStableFD(fd int) ([]byte, syscall.Stat_t, error) {
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return nil, before, err
	}
	if before.Size <= 0 || before.Size > maxRecordBytes {
		return nil, before, deny("record_size_invalid")
	}
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, before, err
	}
	file := os.NewFile(uintptr(duplicate), "release-record")
	if file == nil {
		syscall.Close(duplicate)
		return nil, before, errors.New("wrap record fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, before, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return nil, before, err
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return nil, after, err
	}
	if !sameFileSnapshot(before, after) || int64(len(data)) != after.Size {
		return nil, after, deny("record_changed_or_short_read")
	}
	return data, after, nil
}

func listDirectoryNames(fd int) ([]string, error) {
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), "release-directory")
	if file == nil {
		syscall.Close(duplicate)
		return nil, errors.New("wrap directory fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	Names := make([]string, 0, len(entries))
	for _, entry := range entries {
		Names = append(Names, entry.Name())
	}
	sort.Strings(Names)
	return Names, nil
}

func openTrustedRoot(policy pathPolicy) (int, syscall.Stat_t, error) {
	fd, stat, err := openAbsoluteDirectory(policy.root, policy.allowedDevices)
	if err != nil {
		return -1, stat, err
	}
	if uint64(stat.Dev) != policy.expectedDevice {
		syscall.Close(fd)
		return -1, stat, deny("journal_root_device_mismatch")
	}
	return fd, stat, nil
}

func openAbsoluteDirectory(path string, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.Contains(path, "//") {
		return -1, syscall.Stat_t{}, deny("journal_root_not_absolute_canonical")
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(components) == 1 && components[0] == "" {
		components = nil
	}
	current, err := syscall.Open("/", syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, err
	}
	stat, err := validateDirectoryFD(current, allowed)
	if err != nil {
		syscall.Close(current)
		return -1, stat, err
	}
	for _, component := range components {
		if !safePathComponent(component) {
			syscall.Close(current)
			return -1, stat, deny("journal_root_component_invalid")
		}
		next, openErr := openAt2(current, component, syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
		syscall.Close(current)
		if openErr != nil {
			return -1, stat, denyErr("journal_root_component_open_failed", openErr)
		}
		current = next
		stat, err = validateDirectoryFD(current, allowed)
		if err != nil {
			syscall.Close(current)
			return -1, stat, err
		}
	}
	return current, stat, nil
}

func openJournalDirectory(parentFD int, name string, expectedDevice uint64, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	if !safePathComponent(name) {
		return -1, syscall.Stat_t{}, deny("journal_directory_name_invalid")
	}
	// This lookup is exactly one validated direct child. O_NOFOLLOW plus the
	// retained parent FD and the identity/ownership/mode/device checks below
	// provide the required binding without openat2's false ENOENT observed for
	// immediately renamed hidden directories on supported kernels.
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, denyErr("journal_directory_open_failed", err)
	}
	stat, err := validateDirectoryFD(fd, allowed)
	if err != nil {
		syscall.Close(fd)
		return -1, stat, err
	}
	if stat.Mode&07777 != 0700 {
		syscall.Close(fd)
		return -1, stat, deny("journal_directory_mode_not_0700")
	}
	if uint64(stat.Dev) != expectedDevice {
		syscall.Close(fd)
		return -1, stat, deny("journal_directory_device_mismatch")
	}
	return fd, stat, nil
}

func validateDirectoryFD(fd int, allowed map[uint64]struct{}) (syscall.Stat_t, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return stat, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Uid != 0 || stat.Mode&0022 != 0 {
		return stat, deny("directory_trust_invalid")
	}
	if _, ok := allowed[uint64(stat.Dev)]; !ok {
		return stat, deny("directory_device_not_allowed")
	}
	return stat, nil
}

func validateDirectoryIdentity(fd int, expected syscall.Stat_t, allowed map[uint64]struct{}) error {
	actual, err := validateDirectoryFD(fd, allowed)
	if err != nil {
		return err
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino || actual.Uid != expected.Uid || actual.Mode != expected.Mode {
		return deny("directory_identity_changed")
	}
	return nil
}

func safePathComponent(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.Contains(name, "/") {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func openAt2(parentFD int, name string, flags int, mode uint32) (int, error) {
	if !safePathComponent(name) {
		return -1, deny("openat2_component_invalid")
	}
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return -1, err
	}
	how := openHow{Flags: uint64(flags), Mode: uint64(mode), Resolve: resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks}
	fd, _, errno := syscall.Syscall6(linuxSYSOpenat2, uintptr(parentFD), uintptr(unsafe.Pointer(pointer)), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func mkdirAt(parentFD int, name string, mode uint32) error {
	if !safePathComponent(name) {
		return deny("mkdir_component_invalid")
	}
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_MKDIRAT, uintptr(parentFD), uintptr(unsafe.Pointer(pointer)), uintptr(mode))
	if errno != 0 {
		return errno
	}
	return nil
}

func renameAt2NoReplace(oldDir int, oldName string, newDir int, newName string) error {
	if !safePathComponent(oldName) || !safePathComponent(newName) {
		return deny("rename_component_invalid")
	}
	oldPointer, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPointer, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(linuxSYSRenameat2, uintptr(oldDir), uintptr(unsafe.Pointer(oldPointer)), uintptr(newDir), uintptr(unsafe.Pointer(newPointer)), renameNoReplace, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func randomHex(byteCount int) (string, error) {
	data := make([]byte, byteCount)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", data), nil
}

func writeFull(fd int, data []byte) error {
	for len(data) > 0 {
		count, err := releaseWrite(fd, data)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if count <= 0 {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func sameFileIdentity(a, b syscall.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Uid == b.Uid && a.Gid == b.Gid && a.Mode == b.Mode && a.Nlink == b.Nlink
}
func sameFileSnapshot(a, b syscall.Stat_t) bool {
	return sameFileIdentity(a, b) && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func releaseErrnoIs(err error, target syscall.Errno) bool {
	for err != nil {
		if errno, ok := err.(syscall.Errno); ok {
			return errno == target
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
