//go:build linux && (amd64 || arm64)

// [INPUT]: 只依赖 Go 标准库，依赖同包 model.go 的日志模型与状态机、store_journal_linux.go 的准备与推进、store_fs_linux.go 的可信文件系统原语
// [OUTPUT]: 对外提供 RunCLI（cmd/pandora-release-journal 的薄适配层调用它）；包内提供退出码、系统调用常量、故障注入点（releaseWrite / releaseFdatasync / releaseFsync / releaseRename）、stage 与 temp 名称文法、pathPolicy 与错误模型
// [POS]: platform/releasejournal 发布日志 v1 命令行的入口：prepare / inspect / advance 三个子命令的参数解析、设备白名单与证据 fd 读取；只记录控制器已持久准备或观察到的事实，不能执行 SQL、服务管理、网络、删除或回滚
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package releasejournal

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
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
