//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

const (
	exitUsage  = 64
	exitDenied = 65
	exitSystem = 70

	linuxSYSOpenat2     = 437
	linuxONoFollow      = 0x20000
	linuxOCloExec       = 0x80000
	linuxODirectory     = 0x10000
	resolveNoMagicLinks = 0x02
	resolveNoSymlinks   = 0x04
	resolveBeneath      = 0x08
	renameNoReplace     = 1

	maxJournalBytes = 1 << 20
	maxLineBytes    = 4096
)

var (
	runIDRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	sqlIDRE     = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	candidateRE = regexp.MustCompile(`^U43-(0[1-9]|1[0-9])$`)
	hex64RE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	journalRE   = regexp.MustCompile(`^(U43-(0[1-9]|1[0-9]))\.([0-9a-f]{64})\.journal$`)

	journalWrite     = syscall.Write
	journalFdatasync = syscall.Fdatasync
	journalFsync     = syscall.Fsync
	journalRename    = renameAt2NoReplace
)

type openHow struct {
	Flags   uint64
	Mode    uint64
	Resolve uint64
}

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

func (e *policyError) Unwrap() error { return e.err }

type pathOptions struct {
	journalRoot     string
	expectedRootDev uint64
	allowedDevices  map[uint64]struct{}
	runID           string
	journalBasename string
	expectedJournal string
}

type parsedJournal struct {
	records       []map[string]string
	lastType      string
	lastHash      string
	fullSHA256    string
	journalID     string
	runID         string
	candidate     string
	table         string
	indexName     string
	indexOID      string
	catalogSHA    string
	indexdefSHA   string
	predicateSHA  string
	dependencySHA string
}

type journalSnapshot struct {
	parsed      parsedJournal
	names       []string
	recordBytes [][]byte
	manifestSHA string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return exitUsage
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "pandora-cic-journal: trust denied: euid_zero_required")
		return exitDenied
	}

	var err error
	switch args[0] {
	case "create-intent":
		err = createIntent(args[1:])
	case "append-catalog":
		err = appendCatalog(args[1:])
	case "append-drop":
		err = appendDrop(args[1:])
	case "append-close":
		err = appendClose(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "pandora-cic-journal: unknown command %q\n", args[0])
		printUsage()
		return exitUsage
	}
	if err == nil {
		return 0
	}
	var denied *policyError
	if errors.As(err, &denied) {
		fmt.Fprintf(os.Stderr, "pandora-cic-journal: trust denied: %s\n", denied)
		return exitDenied
	}
	var usage *usageError
	if errors.As(err, &usage) {
		fmt.Fprintf(os.Stderr, "pandora-cic-journal: usage: %s\n", usage)
		return exitUsage
	}
	fmt.Fprintf(os.Stderr, "pandora-cic-journal: system failure: %v\n", err)
	return exitSystem
}

type usageError struct{ reason string }

func (e *usageError) Error() string { return e.reason }

func usage(reason string) error { return &usageError{reason: reason} }
func deny(reason string) error  { return &policyError{reason: reason} }

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: pandora-cic-journal create-intent|append-catalog|append-drop|append-close [flags]")
	fmt.Fprintln(os.Stderr, "all commands require --journal-root, --expected-root-device, --allow-devices, and EUID 0")
	fmt.Fprintln(os.Stderr, "append commands additionally require --run-id, --journal, and --expect-journal-sha256")
}

func addPathFlags(fs *flag.FlagSet, appendMode bool) (*string, *string, *string, *string, *string, *string) {
	root := fs.String("journal-root", "", "release-manifest-fixed absolute journal root")
	rootDev := fs.String("expected-root-device", "", "exact decimal st_dev for journal root")
	devices := fs.String("allow-devices", "", "strictly sorted comma-separated allowed st_dev values")
	runID := fs.String("run-id", "", "frozen safe run identifier")
	journal := fs.String("journal", "", "generated journal basename (append only)")
	expected := fs.String("expect-journal-sha256", "", "current whole journal SHA-256 (append only)")
	if !appendMode {
		_ = journal
		_ = expected
	}
	return root, rootDev, devices, runID, journal, expected
}

func parsePathOptions(root, rootDev, devices, runID, journal, expected string, appendMode bool) (pathOptions, error) {
	if root == "" || rootDev == "" || devices == "" || runID == "" {
		return pathOptions{}, usage("required path identity flag missing")
	}
	if !runIDRE.MatchString(runID) || runID == "." || runID == ".." {
		return pathOptions{}, deny("run_id_invalid")
	}
	dev, err := parsePositiveUint(rootDev)
	if err != nil {
		return pathOptions{}, deny("expected_root_device_invalid")
	}
	allowed, err := parseAllowedDevices(devices)
	if err != nil {
		return pathOptions{}, deny("allowed_devices_invalid")
	}
	if _, ok := allowed[dev]; !ok {
		return pathOptions{}, deny("expected_root_device_not_allowed")
	}
	if appendMode {
		if !journalRE.MatchString(journal) {
			return pathOptions{}, deny("journal_basename_invalid")
		}
		if !hex64RE.MatchString(expected) {
			return pathOptions{}, deny("expected_journal_sha256_invalid")
		}
	} else if journal != "" || expected != "" {
		return pathOptions{}, usage("create-intent does not accept journal append flags")
	}
	return pathOptions{
		journalRoot:     root,
		expectedRootDev: dev,
		allowedDevices:  allowed,
		runID:           runID,
		journalBasename: journal,
		expectedJournal: expected,
	}, nil
}

func createIntent(args []string) error {
	fs := flag.NewFlagSet("create-intent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, rootDev, devices, runID, journal, expected := addPathFlags(fs, false)
	created := fs.String("created-at-epoch", "", "positive canonical epoch seconds")
	releaseSHA := fs.String("release-manifest-sha256", "", "release manifest SHA-256")
	runnerSHA := fs.String("runner-sha256", "", "runner SHA-256")
	migrationSHA := fs.String("migration-sha256", "", "migration SHA-256")
	sourceSystem := fs.String("source-system-identifier", "", "positive PostgreSQL system identifier")
	databaseName := fs.String("database-name", "", "canonical database identifier")
	databaseOID := fs.String("database-oid", "", "positive database OID")
	candidate := fs.String("candidate", "", "U43-01 through U43-19")
	table := fs.String("table", "", "frozen table identifier")
	indexName := fs.String("index-name", "", "frozen index identifier")
	indexdefSHA := fs.String("expected-indexdef-sha256", "", "expected index definition SHA-256")
	predicateSHA := fs.String("expected-predicate-sha256", "", "expected predicate SHA-256")
	dependencySHA := fs.String("expected-dependency-sha256", "", "expected dependency manifest SHA-256")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("invalid create-intent flags")
	}
	paths, err := parsePathOptions(*root, *rootDev, *devices, *runID, *journal, *expected, false)
	if err != nil {
		return err
	}
	if err := requireEpoch(*created); err != nil {
		return deny("created_at_epoch_invalid")
	}
	if !hex64RE.MatchString(*releaseSHA) || !hex64RE.MatchString(*runnerSHA) ||
		!hex64RE.MatchString(*migrationSHA) || !hex64RE.MatchString(*indexdefSHA) ||
		!hex64RE.MatchString(*predicateSHA) || !hex64RE.MatchString(*dependencySHA) {
		return deny("intent_sha256_invalid")
	}
	if _, err := parsePositiveUint(*sourceSystem); err != nil {
		return deny("source_system_identifier_invalid")
	}
	if !sqlIDRE.MatchString(*databaseName) {
		return deny("database_name_invalid")
	}
	if _, err := parsePositiveUint(*databaseOID); err != nil {
		return deny("database_oid_invalid")
	}
	if !candidateRE.MatchString(*candidate) || !sqlIDRE.MatchString(*table) || !sqlIDRE.MatchString(*indexName) {
		return deny("candidate_table_or_index_invalid")
	}

	journalID, err := randomHex(32)
	if err != nil {
		return fmt.Errorf("generate journal id: %w", err)
	}
	body := canonicalBody([]field{
		{"format", "client-auth-00043-cleanup-journal-v1"},
		{"record", "intent"},
		{"journal_id", journalID},
		{"created_at_epoch", *created},
		{"run_id", paths.runID},
		{"release_manifest_sha256", *releaseSHA},
		{"runner_sha256", *runnerSHA},
		{"migration_sha256", *migrationSHA},
		{"source_system_identifier", *sourceSystem},
		{"database_name", *databaseName},
		{"database_oid", *databaseOID},
		{"candidate", *candidate},
		{"table", *table},
		{"index_name", *indexName},
		{"expected_indexdef_sha256", *indexdefSHA},
		{"expected_predicate_sha256", *predicateSHA},
		{"expected_dependency_sha256", *dependencySHA},
		{"attempt", "1"},
		{"status", "intent_fsynced"},
	})
	recordSHA := digestBytes(body)
	content := appendRecordHash(body, recordSHA)
	basename := *candidate + "." + journalID + ".journal"
	fullSHA, err := publishIntent(paths, basename, content)
	if err != nil {
		return err
	}
	fmt.Printf("PANDORA_CIC_JOURNAL_V1 action=create-intent state=INTENT_FSYNCED journal_id=%s journal=%s journal_sha256=%s record_sha256=%s\n",
		journalID, basename, fullSHA, recordSHA)
	return nil
}

func appendCatalog(args []string) error {
	fs := flag.NewFlagSet("append-catalog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, rootDev, devices, runID, journal, expected := addPathFlags(fs, true)
	observed := fs.String("observed-at-epoch", "", "positive canonical epoch seconds")
	tableOID := fs.String("table-oid", "", "positive table OID")
	indexOID := fs.String("index-oid", "", "positive index OID")
	constraintOID := fs.String("constraint-oid", "", "must be canonical zero")
	catalogSHA := fs.String("catalog-sha256", "", "canonical live catalog row SHA-256")
	indexdefSHA := fs.String("indexdef-sha256", "", "live index definition SHA-256")
	predicateSHA := fs.String("predicate-sha256", "", "live predicate SHA-256")
	dependencySHA := fs.String("dependency-sha256", "", "live canonical dependencies SHA-256")
	classifier := fs.String("classifier", "", "must be INVALID_EXACT")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("invalid append-catalog flags")
	}
	paths, err := parsePathOptions(*root, *rootDev, *devices, *runID, *journal, *expected, true)
	if err != nil {
		return err
	}
	if requireEpoch(*observed) != nil {
		return deny("observed_at_epoch_invalid")
	}
	if _, err := parsePositiveUint(*tableOID); err != nil {
		return deny("table_oid_invalid")
	}
	if _, err := parsePositiveUint(*indexOID); err != nil {
		return deny("index_oid_invalid")
	}
	if *constraintOID != "0" || *classifier != "INVALID_EXACT" {
		return deny("catalog_classifier_or_constraint_invalid")
	}
	for _, value := range []string{*catalogSHA, *indexdefSHA, *predicateSHA, *dependencySHA} {
		if !hex64RE.MatchString(value) {
			return deny("catalog_sha256_invalid")
		}
	}
	return appendCanonical(paths, "catalog", func(j parsedJournal) ([]byte, error) {
		if j.lastType != "intent" {
			return nil, deny("catalog_transition_invalid")
		}
		if *indexdefSHA != j.indexdefSHA || *predicateSHA != j.predicateSHA || *dependencySHA != j.dependencySHA {
			return nil, deny("catalog_expected_hash_mismatch")
		}
		return canonicalBody([]field{
			{"record", "catalog"},
			{"observed_at_epoch", *observed},
			{"journal_id", j.journalID},
			{"run_id", j.runID},
			{"candidate", j.candidate},
			{"table_oid", *tableOID},
			{"index_oid", *indexOID},
			{"constraint_oid", "0"},
			{"catalog_sha256", *catalogSHA},
			{"indexdef_sha256", *indexdefSHA},
			{"predicate_sha256", *predicateSHA},
			{"dependency_sha256", *dependencySHA},
			{"classifier", "INVALID_EXACT"},
			{"status", "catalog_fsynced"},
		}), nil
	})
}

func appendDrop(args []string) error {
	fs := flag.NewFlagSet("append-drop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, rootDev, devices, runID, journal, expected := addPathFlags(fs, true)
	dropped := fs.String("dropped-at-epoch", "", "positive canonical epoch seconds")
	indexOID := fs.String("index-oid", "", "old index OID")
	catalogSHA := fs.String("catalog-sha256", "", "prior exact catalog SHA-256")
	classifier := fs.String("classifier", "", "must be REMOVED_EXACT")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("invalid append-drop flags")
	}
	paths, err := parsePathOptions(*root, *rootDev, *devices, *runID, *journal, *expected, true)
	if err != nil {
		return err
	}
	if requireEpoch(*dropped) != nil || *classifier != "REMOVED_EXACT" {
		return deny("drop_epoch_or_classifier_invalid")
	}
	if _, err := parsePositiveUint(*indexOID); err != nil {
		return deny("drop_index_oid_invalid")
	}
	if !hex64RE.MatchString(*catalogSHA) {
		return deny("drop_catalog_sha256_invalid")
	}
	return appendCanonical(paths, "drop", func(j parsedJournal) ([]byte, error) {
		if j.lastType != "catalog" || *indexOID != j.indexOID || *catalogSHA != j.catalogSHA {
			return nil, deny("drop_transition_or_catalog_identity_invalid")
		}
		return canonicalBody([]field{
			{"record", "drop"},
			{"dropped_at_epoch", *dropped},
			{"journal_id", j.journalID},
			{"run_id", j.runID},
			{"candidate", j.candidate},
			{"index_oid", j.indexOID},
			{"catalog_sha256", j.catalogSHA},
			{"classifier", "REMOVED_EXACT"},
			{"status", "drop_fsynced"},
		}), nil
	})
}

func appendClose(args []string) error {
	fs := flag.NewFlagSet("append-close", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, rootDev, devices, runID, journal, expected := addPathFlags(fs, true)
	closed := fs.String("closed-at-epoch", "", "positive canonical epoch seconds")
	classifier := fs.String("classifier", "", "must be REMOVED_EXACT")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("invalid append-close flags")
	}
	paths, err := parsePathOptions(*root, *rootDev, *devices, *runID, *journal, *expected, true)
	if err != nil {
		return err
	}
	if requireEpoch(*closed) != nil || *classifier != "REMOVED_EXACT" {
		return deny("close_epoch_or_classifier_invalid")
	}
	return appendCanonical(paths, "close", func(j parsedJournal) ([]byte, error) {
		if j.lastType != "catalog" && j.lastType != "drop" {
			return nil, deny("close_transition_invalid")
		}
		return canonicalBody([]field{
			{"record", "close"},
			{"closed_at_epoch", *closed},
			{"journal_id", j.journalID},
			{"run_id", j.runID},
			{"candidate", j.candidate},
			{"former_index_oid", j.indexOID},
			{"classifier", "REMOVED_EXACT"},
			{"status", "closed"},
		}), nil
	})
}

type field struct {
	key   string
	value string
}

func canonicalBody(fields []field) []byte {
	var out strings.Builder
	for _, item := range fields {
		out.WriteString(item.key)
		out.WriteByte('=')
		out.WriteString(item.value)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

func appendRecordHash(body []byte, digest string) []byte {
	result := make([]byte, 0, len(body)+len("record_sha256=")+64+1)
	result = append(result, body...)
	result = append(result, "record_sha256="...)
	result = append(result, digest...)
	result = append(result, '\n')
	return result
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func chainedDigest(previous string, body []byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, previous)
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}

func randomHex(count int) (string, error) {
	data := make([]byte, count)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func requireEpoch(value string) error {
	_, err := parsePositiveUint(value)
	return err
}

func parsePositiveUint(value string) (uint64, error) {
	if value == "" || value == "0" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("not canonical positive integer")
	}
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(number, 10) != value {
		return 0, errors.New("not canonical positive integer")
	}
	return number, nil
}

func parseAllowedDevices(csv string) (map[uint64]struct{}, error) {
	if csv == "" {
		return nil, errors.New("empty allowlist")
	}
	allowed := make(map[uint64]struct{})
	var previous uint64
	for index, raw := range strings.Split(csv, ",") {
		value, err := parsePositiveUint(raw)
		if err != nil || (index > 0 && value <= previous) {
			return nil, errors.New("allowlist must be strictly sorted canonical positive integers")
		}
		allowed[value] = struct{}{}
		previous = value
	}
	return allowed, nil
}

func publishIntent(options pathOptions, basename string, content []byte) (string, error) {
	rootFD, rootStat, err := openTrustedRoot(options)
	if err != nil {
		return "", err
	}
	defer syscall.Close(rootFD)

	runFD, runStat, err := openTrustedDirectoryAt(rootFD, options.runID, options.allowedDevices)
	if err != nil {
		return "", err
	}
	defer syscall.Close(runFD)

	stageNonce, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("generate stage nonce: %w", err)
	}
	stageName := "." + basename + ".stage." + stageNonce
	if err := syscall.Mkdirat(runFD, stageName, 0700); err != nil {
		return "", &policyError{reason: "journal_stage_directory_create_failed", err: err}
	}
	stageDirFD, stageDirStat, err := openTrustedDirectoryAt(runFD, stageName, options.allowedDevices)
	if err != nil {
		return "", err
	}
	defer syscall.Close(stageDirFD)
	if stageDirStat.Mode&07777 != 0700 || uint64(stageDirStat.Dev) != uint64(runStat.Dev) {
		return "", deny("journal_stage_cross_device")
	}
	if err := writeImmutableRecordAt(stageDirFD, uint64(stageDirStat.Dev), "000.intent.record", content); err != nil {
		return "", err
	}
	if err := journalFsync(stageDirFD); err != nil {
		return "", fmt.Errorf("fsync journal stage directory: %w", err)
	}
	if err := validateDirectoryIdentity(runFD, runStat, options.allowedDevices); err != nil {
		return "", err
	}
	if err := journalRename(runFD, stageName, runFD, basename); err != nil {
		return "", &policyError{reason: "journal_publish_noreplace_failed", err: err}
	}
	if err := journalFsync(runFD); err != nil {
		return "", fmt.Errorf("fsync journal directory after publish: %w", err)
	}
	if err := validateDirectoryIdentity(rootFD, rootStat, options.allowedDevices); err != nil {
		return "", err
	}
	if err := validateDirectoryIdentity(runFD, runStat, options.allowedDevices); err != nil {
		return "", err
	}

	finalFD, finalDirStat, err := openTrustedDirectoryAt(runFD, basename, options.allowedDevices)
	if err != nil {
		return "", &policyError{reason: "published_journal_reopen_failed", err: err}
	}
	defer syscall.Close(finalFD)
	if finalDirStat.Mode&07777 != 0700 || uint64(finalDirStat.Dev) != uint64(runStat.Dev) {
		return "", deny("published_journal_cross_device")
	}
	snapshot, err := loadJournalDirectory(finalFD, uint64(finalDirStat.Dev))
	if err != nil {
		return "", err
	}
	parsed := snapshot.parsed
	if parsed.lastType != "intent" || parsed.runID != options.runID || parsed.journalID == "" {
		return "", deny("published_intent_reparse_failed")
	}
	if err := validateJournalName(basename, parsed); err != nil {
		return "", err
	}
	return snapshot.manifestSHA, nil
}

func appendCanonical(options pathOptions, action string, makeBody func(parsedJournal) ([]byte, error)) error {
	rootFD, rootStat, err := openTrustedRoot(options)
	if err != nil {
		return err
	}
	defer syscall.Close(rootFD)
	runFD, runStat, err := openTrustedDirectoryAt(rootFD, options.runID, options.allowedDevices)
	if err != nil {
		return err
	}
	defer syscall.Close(runFD)

	journalFD, journalDirStat, err := openTrustedDirectoryAt(runFD, options.journalBasename, options.allowedDevices)
	if err != nil {
		return &policyError{reason: "journal_append_open_failed", err: err}
	}
	defer syscall.Close(journalFD)
	if err := syscall.Flock(journalFD, syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock journal: %w", err)
	}
	defer syscall.Flock(journalFD, syscall.LOCK_UN)

	if journalDirStat.Mode&07777 != 0700 || uint64(journalDirStat.Dev) != uint64(runStat.Dev) {
		return deny("journal_directory_cross_device")
	}
	snapshot, err := loadJournalDirectory(journalFD, uint64(journalDirStat.Dev))
	if err != nil {
		return err
	}
	parsed := snapshot.parsed
	if parsed.runID != options.runID {
		return deny("journal_run_id_mismatch")
	}
	if err := validateJournalName(options.journalBasename, parsed); err != nil {
		return err
	}
	if parsed.lastType == action {
		return recoverPublishedAppend(options, action, runFD, runStat, journalFD, journalDirStat, snapshot, makeBody)
	}
	if snapshot.manifestSHA != options.expectedJournal {
		return deny("expected_journal_sha256_mismatch")
	}
	body, err := makeBody(parsed)
	if err != nil {
		return err
	}
	recordSHA := chainedDigest(parsed.lastHash, body)
	record := appendRecordHash(body, recordSHA)
	segmentName, err := segmentNameForAction(action)
	if err != nil {
		return err
	}
	stageNonce, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("generate append stage nonce: %w", err)
	}
	stageName := "." + options.journalBasename + "." + action + ".stage." + stageNonce
	if err := writeImmutableRecordAt(runFD, uint64(runStat.Dev), stageName, record); err != nil {
		return err
	}
	if err := journalRename(runFD, stageName, journalFD, segmentName); err != nil {
		return &policyError{reason: "journal_segment_publish_noreplace_failed", err: err}
	}
	if err := journalFsync(runFD); err != nil {
		return fmt.Errorf("fsync run directory after segment publish: %w", err)
	}
	if err := journalFsync(journalFD); err != nil {
		return fmt.Errorf("fsync journal directory after segment publish: %w", err)
	}
	reloaded, err := loadJournalDirectory(journalFD, uint64(journalDirStat.Dev))
	if err != nil {
		return err
	}
	if reloaded.parsed.lastType != action || reloaded.parsed.lastHash != recordSHA ||
		len(reloaded.parsed.records) != len(parsed.records)+1 {
		return deny("journal_post_publish_reparse_failed")
	}
	if err := validateDirectoryIdentity(rootFD, rootStat, options.allowedDevices); err != nil {
		return err
	}
	if err := validateDirectoryIdentity(runFD, runStat, options.allowedDevices); err != nil {
		return err
	}
	fmt.Printf("PANDORA_CIC_JOURNAL_V1 action=append-%s state=%s journal=%s journal_sha256=%s record_sha256=%s\n",
		action, stateForRecord(action), options.journalBasename, reloaded.manifestSHA, recordSHA)
	return nil
}

func recoverPublishedAppend(
	options pathOptions,
	action string,
	runFD int,
	runStat syscall.Stat_t,
	journalFD int,
	journalDirStat syscall.Stat_t,
	snapshot journalSnapshot,
	makeBody func(parsedJournal) ([]byte, error),
) error {
	if len(snapshot.recordBytes) < 2 {
		return deny("published_append_without_prior_record")
	}
	prefix, err := snapshotPrefix(snapshot, len(snapshot.recordBytes)-1)
	if err != nil {
		return err
	}
	if prefix.manifestSHA != options.expectedJournal {
		return deny("expected_journal_sha256_mismatch")
	}
	body, err := makeBody(prefix.parsed)
	if err != nil {
		return err
	}
	recordSHA := chainedDigest(prefix.parsed.lastHash, body)
	expectedRecord := appendRecordHash(body, recordSHA)
	if !bytes.Equal(expectedRecord, snapshot.recordBytes[len(snapshot.recordBytes)-1]) {
		return deny("published_append_retry_content_mismatch")
	}
	segmentName, err := segmentNameForAction(action)
	if err != nil {
		return err
	}
	recordFD, err := openAt2(journalFD, segmentName, syscall.O_RDONLY|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return &policyError{reason: "published_append_retry_open_failed", err: err}
	}
	defer syscall.Close(recordFD)
	var recordStat syscall.Stat_t
	if err := syscall.Fstat(recordFD, &recordStat); err != nil {
		return fmt.Errorf("fstat published append retry: %w", err)
	}
	if err := validateJournalStat(recordStat, uint64(journalDirStat.Dev)); err != nil {
		return err
	}
	if err := journalFsync(recordFD); err != nil {
		return fmt.Errorf("fsync published append retry record: %w", err)
	}
	if err := journalFsync(runFD); err != nil {
		return fmt.Errorf("fsync published append retry source directory: %w", err)
	}
	if err := journalFsync(journalFD); err != nil {
		return fmt.Errorf("fsync published append retry directory: %w", err)
	}
	if err := validateDirectoryIdentity(runFD, runStat, options.allowedDevices); err != nil {
		return err
	}
	if err := validateDirectoryIdentity(journalFD, journalDirStat, options.allowedDevices); err != nil {
		return err
	}
	reloaded, err := loadJournalDirectory(journalFD, uint64(journalDirStat.Dev))
	if err != nil {
		return err
	}
	if reloaded.manifestSHA != snapshot.manifestSHA || reloaded.parsed.lastHash != recordSHA {
		return deny("published_append_retry_revalidation_failed")
	}
	fmt.Printf("PANDORA_CIC_JOURNAL_V1 action=append-%s state=%s journal=%s journal_sha256=%s record_sha256=%s recovered=true\n",
		action, stateForRecord(action), options.journalBasename, reloaded.manifestSHA, recordSHA)
	return nil
}

func snapshotPrefix(snapshot journalSnapshot, count int) (journalSnapshot, error) {
	if count <= 0 || count > len(snapshot.recordBytes) {
		return journalSnapshot{}, deny("journal_prefix_count_invalid")
	}
	names := append([]string(nil), snapshot.names[:count]...)
	records := make([][]byte, count)
	var combined []byte
	for index := 0; index < count; index++ {
		records[index] = append([]byte(nil), snapshot.recordBytes[index]...)
		combined = append(combined, records[index]...)
	}
	parsed, err := parseJournal(combined)
	if err != nil {
		return journalSnapshot{}, err
	}
	manifest := manifestDigest(names, records, parsed.lastHash)
	parsed.fullSHA256 = manifest
	return journalSnapshot{parsed: parsed, names: names, recordBytes: records, manifestSHA: manifest}, nil
}

func segmentNameForAction(action string) (string, error) {
	switch action {
	case "intent":
		return "000.intent.record", nil
	case "catalog":
		return "010.catalog.record", nil
	case "drop":
		return "020.drop.record", nil
	case "close":
		return "030.close.record", nil
	default:
		return "", deny("unknown_journal_action")
	}
}

func writeImmutableRecordAt(parentFD int, parentDev uint64, name string, content []byte) error {
	fd, err := openAt2(parentFD, name,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|linuxONoFollow|linuxOCloExec, 0600)
	if err != nil {
		return &policyError{reason: "immutable_record_create_failed", err: err}
	}
	defer syscall.Close(fd)
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return fmt.Errorf("fstat immutable record: %w", err)
	}
	if err := validateJournalStat(before, parentDev); err != nil {
		return err
	}
	if err := writeFull(fd, content); err != nil {
		return fmt.Errorf("write immutable record: %w", err)
	}
	if err := journalFdatasync(fd); err != nil {
		return fmt.Errorf("fdatasync immutable record: %w", err)
	}
	if err := journalFsync(fd); err != nil {
		return fmt.Errorf("fsync immutable record: %w", err)
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return fmt.Errorf("post-sync fstat immutable record: %w", err)
	}
	if !sameFileIdentity(before, after) || after.Size != int64(len(content)) {
		return deny("immutable_record_identity_or_size_changed")
	}
	return nil
}

func loadJournalDirectory(journalFD int, directoryDev uint64) (journalSnapshot, error) {
	names, err := listDirectoryNames(journalFD)
	if err != nil {
		return journalSnapshot{}, err
	}
	if len(names) == 0 || len(names) > 4 {
		return journalSnapshot{}, deny("journal_segment_count_invalid")
	}
	sort.Strings(names)
	allowedNames := map[string]struct{}{
		"000.intent.record":  {},
		"010.catalog.record": {},
		"020.drop.record":    {},
		"030.close.record":   {},
	}
	recordBytes := make([][]byte, 0, len(names))
	var combined []byte
	for _, name := range names {
		if _, ok := allowedNames[name]; !ok {
			return journalSnapshot{}, deny("journal_unknown_segment")
		}
		fd, err := openAt2(journalFD, name, syscall.O_RDONLY|linuxONoFollow|linuxOCloExec, 0)
		if err != nil {
			return journalSnapshot{}, &policyError{reason: "journal_segment_open_failed", err: err}
		}
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil {
			syscall.Close(fd)
			return journalSnapshot{}, fmt.Errorf("fstat journal segment: %w", err)
		}
		if err := validateJournalStat(stat, directoryDev); err != nil {
			syscall.Close(fd)
			return journalSnapshot{}, err
		}
		data, snapshot, err := readJournalFD(fd)
		syscall.Close(fd)
		if err != nil {
			return journalSnapshot{}, err
		}
		if !sameFileSnapshot(stat, snapshot) {
			return journalSnapshot{}, deny("journal_segment_changed_while_reading")
		}
		recordBytes = append(recordBytes, data)
		combined = append(combined, data...)
	}
	parsed, err := parseJournal(combined)
	if err != nil {
		return journalSnapshot{}, err
	}
	if len(parsed.records) != len(names) {
		return journalSnapshot{}, deny("journal_segment_record_count_mismatch")
	}
	for index, record := range parsed.records {
		expectedName, err := segmentNameForAction(record["record"])
		if err != nil || names[index] != expectedName {
			return journalSnapshot{}, deny("journal_segment_name_phase_mismatch")
		}
	}
	manifest := manifestDigest(names, recordBytes, parsed.lastHash)
	parsed.fullSHA256 = manifest
	return journalSnapshot{
		parsed:      parsed,
		names:       names,
		recordBytes: recordBytes,
		manifestSHA: manifest,
	}, nil
}

func listDirectoryNames(fd int) ([]string, error) {
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("dup journal directory: %w", err)
	}
	file := os.NewFile(uintptr(duplicate), "pandora-cic-journal-directory")
	if file == nil {
		syscall.Close(duplicate)
		return nil, errors.New("cannot wrap duplicated journal directory fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind journal directory: %w", err)
	}
	names, err := file.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("list journal directory: %w", err)
	}
	return names, nil
}

func manifestDigest(names []string, records [][]byte, headHash string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "format=client-auth-00043-cleanup-journal-manifest-v1\n")
	for index, name := range names {
		_, _ = fmt.Fprintf(hash, "segment=%s sha256=%s\n", name, digestBytes(records[index]))
	}
	_, _ = fmt.Fprintf(hash, "head_record_sha256=%s\n", headHash)
	return hex.EncodeToString(hash.Sum(nil))
}

func stateForRecord(record string) string {
	switch record {
	case "catalog":
		return "CATALOG_FSYNCED"
	case "drop":
		return "DROP_FSYNCED"
	case "close":
		return "CLOSED"
	default:
		return "UNKNOWN"
	}
}

func openTrustedRoot(options pathOptions) (int, syscall.Stat_t, error) {
	fd, stat, err := openAbsoluteDirectory(options.journalRoot, options.allowedDevices)
	if err != nil {
		return -1, syscall.Stat_t{}, err
	}
	if uint64(stat.Dev) != options.expectedRootDev {
		syscall.Close(fd)
		return -1, syscall.Stat_t{}, deny("journal_root_device_mismatch")
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
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return -1, syscall.Stat_t{}, deny("journal_root_component_invalid")
		}
	}
	current, err := syscall.Open("/", syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, fmt.Errorf("open filesystem root: %w", err)
	}
	stat, err := validateDirectoryFD(current, allowed)
	if err != nil {
		syscall.Close(current)
		return -1, syscall.Stat_t{}, err
	}
	for _, component := range components {
		next, err := openAt2(current, component,
			syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
		if err != nil {
			syscall.Close(current)
			return -1, syscall.Stat_t{}, &policyError{reason: "journal_root_component_open_failed", err: err}
		}
		syscall.Close(current)
		current = next
		stat, err = validateDirectoryFD(current, allowed)
		if err != nil {
			syscall.Close(current)
			return -1, syscall.Stat_t{}, err
		}
	}
	return current, stat, nil
}

func openTrustedDirectoryAt(parentFD int, name string, allowed map[uint64]struct{}) (int, syscall.Stat_t, error) {
	if !safePathComponent(name) {
		return -1, syscall.Stat_t{}, deny("directory_name_invalid")
	}
	fd, err := openAt2(parentFD, name,
		syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return -1, syscall.Stat_t{}, &policyError{reason: "run_directory_open_failed", err: err}
	}
	stat, err := validateDirectoryFD(fd, allowed)
	if err != nil {
		syscall.Close(fd)
		return -1, syscall.Stat_t{}, err
	}
	return fd, stat, nil
}

func safePathComponent(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.Contains(name, "/") {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func openAt2(parentFD int, name string, flags int, mode uint32) (int, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return -1, deny("openat2_component_invalid")
	}
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return -1, err
	}
	how := openHow{
		Flags:   uint64(flags),
		Mode:    uint64(mode),
		Resolve: resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks,
	}
	fd, _, errno := syscall.Syscall6(
		linuxSYSOpenat2,
		uintptr(parentFD),
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&how)),
		unsafe.Sizeof(how),
		0,
		0,
	)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func renameAt2NoReplace(oldDir int, oldName string, newDir int, newName string) error {
	oldPtr, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPtr, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(
		linuxSYSRenameat2,
		uintptr(oldDir),
		uintptr(unsafe.Pointer(oldPtr)),
		uintptr(newDir),
		uintptr(unsafe.Pointer(newPtr)),
		renameNoReplace,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func validateDirectoryFD(fd int, allowed map[uint64]struct{}) (syscall.Stat_t, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return stat, fmt.Errorf("fstat trusted directory: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return stat, deny("ancestor_not_directory")
	}
	if stat.Uid != 0 {
		return stat, deny("ancestor_not_root_owned")
	}
	if stat.Mode&0022 != 0 {
		return stat, deny("ancestor_group_or_world_writable")
	}
	if _, ok := allowed[uint64(stat.Dev)]; !ok {
		return stat, deny("ancestor_device_not_allowed")
	}
	return stat, nil
}

func validateDirectoryIdentity(fd int, expected syscall.Stat_t, allowed map[uint64]struct{}) error {
	actual, err := validateDirectoryFD(fd, allowed)
	if err != nil {
		return err
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino || actual.Uid != expected.Uid ||
		actual.Mode != expected.Mode {
		return deny("trusted_directory_identity_changed")
	}
	return nil
}

func validateJournalStat(stat syscall.Stat_t, directoryDev uint64) error {
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return deny("journal_not_regular")
	}
	if stat.Uid != 0 {
		return deny("journal_not_root_owned")
	}
	if stat.Mode&07777 != 0600 {
		return deny("journal_mode_not_0600")
	}
	if stat.Nlink != 1 {
		return deny("journal_link_count_not_one")
	}
	if uint64(stat.Dev) != directoryDev {
		return deny("journal_directory_device_mismatch")
	}
	return nil
}

func sameFileIdentity(a, b syscall.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Uid == b.Uid &&
		a.Gid == b.Gid && a.Mode == b.Mode && a.Nlink == b.Nlink
}

func sameFileSnapshot(a, b syscall.Stat_t) bool {
	return sameFileIdentity(a, b) && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func writeFull(fd int, data []byte) error {
	for len(data) > 0 {
		count, err := journalWrite(fd, data)
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

func readJournalFD(fd int) ([]byte, syscall.Stat_t, error) {
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return nil, before, fmt.Errorf("fstat before journal read: %w", err)
	}
	if before.Size <= 0 || before.Size > maxJournalBytes {
		return nil, before, deny("journal_size_invalid")
	}
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, before, fmt.Errorf("dup journal fd: %w", err)
	}
	file := os.NewFile(uintptr(duplicate), "pandora-cic-journal-read")
	if file == nil {
		syscall.Close(duplicate)
		return nil, before, errors.New("cannot wrap duplicated journal fd")
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, before, fmt.Errorf("seek journal: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	if err != nil {
		return nil, before, fmt.Errorf("read journal: %w", err)
	}
	if len(data) > maxJournalBytes {
		return nil, before, deny("journal_too_large")
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return nil, after, fmt.Errorf("fstat after journal read: %w", err)
	}
	if !sameFileSnapshot(before, after) || int64(len(data)) != after.Size {
		return nil, after, deny("journal_changed_or_short_read")
	}
	return data, after, nil
}

var recordSchemas = map[string][]string{
	"intent": {
		"format", "record", "journal_id", "created_at_epoch", "run_id",
		"release_manifest_sha256", "runner_sha256", "migration_sha256",
		"source_system_identifier", "database_name", "database_oid", "candidate",
		"table", "index_name", "expected_indexdef_sha256",
		"expected_predicate_sha256", "expected_dependency_sha256", "attempt",
		"status", "record_sha256",
	},
	"catalog": {
		"record", "observed_at_epoch", "journal_id", "run_id", "candidate",
		"table_oid", "index_oid", "constraint_oid", "catalog_sha256",
		"indexdef_sha256", "predicate_sha256", "dependency_sha256", "classifier",
		"status", "record_sha256",
	},
	"drop": {
		"record", "dropped_at_epoch", "journal_id", "run_id", "candidate",
		"index_oid", "catalog_sha256", "classifier", "status", "record_sha256",
	},
	"close": {
		"record", "closed_at_epoch", "journal_id", "run_id", "candidate",
		"former_index_oid", "classifier", "status", "record_sha256",
	},
}

func parseJournal(data []byte) (parsedJournal, error) {
	var result parsedJournal
	if len(data) == 0 || len(data) > maxJournalBytes {
		return result, deny("journal_size_invalid")
	}
	if !utf8.Valid(data) {
		return result, deny("journal_non_utf8")
	}
	if bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 {
		return result, deny("journal_cr_or_nul")
	}
	if data[len(data)-1] != '\n' {
		return result, deny("journal_missing_final_lf")
	}
	rawLines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(rawLines) == 0 {
		return result, deny("journal_empty")
	}
	lines := make([]string, len(rawLines))
	for index, raw := range rawLines {
		if len(raw) == 0 || len(raw) > maxLineBytes {
			return result, deny("journal_blank_or_overlong_line")
		}
		lines[index] = string(raw)
	}

	offset := 0
	previousHash := ""
	expectedNext := "intent"
	for offset < len(lines) {
		recordType := expectedNext
		if expectedNext == "catalog-or-terminal" {
			key, value, ok := splitCanonicalLine(lines[offset])
			if !ok || key != "record" {
				return result, deny("journal_record_boundary_invalid")
			}
			recordType = value
			if recordType != "catalog" && recordType != "drop" && recordType != "close" {
				return result, deny("journal_unknown_record")
			}
		}
		schema, ok := recordSchemas[recordType]
		if !ok || offset+len(schema) > len(lines) {
			return result, deny("journal_record_truncated")
		}
		recordLines := lines[offset : offset+len(schema)]
		values, body, hashValue, err := parseRecord(recordType, recordLines, schema, previousHash)
		if err != nil {
			return result, err
		}
		if err := validateRecordValues(recordType, values, result); err != nil {
			return result, err
		}
		_ = body
		result.records = append(result.records, values)
		result.lastType = recordType
		result.lastHash = hashValue
		previousHash = hashValue
		offset += len(schema)

		switch recordType {
		case "intent":
			result.journalID = values["journal_id"]
			result.runID = values["run_id"]
			result.candidate = values["candidate"]
			result.table = values["table"]
			result.indexName = values["index_name"]
			result.indexdefSHA = values["expected_indexdef_sha256"]
			result.predicateSHA = values["expected_predicate_sha256"]
			result.dependencySHA = values["expected_dependency_sha256"]
			expectedNext = "catalog-or-terminal"
		case "catalog":
			result.indexOID = values["index_oid"]
			result.catalogSHA = values["catalog_sha256"]
			if offset < len(lines) {
				key, value, ok := splitCanonicalLine(lines[offset])
				if !ok || key != "record" || (value != "drop" && value != "close") {
					return result, deny("journal_transition_after_catalog_invalid")
				}
			}
			expectedNext = "catalog-or-terminal"
		case "drop":
			if offset < len(lines) {
				key, value, ok := splitCanonicalLine(lines[offset])
				if !ok || key != "record" || value != "close" {
					return result, deny("journal_transition_after_drop_invalid")
				}
			}
			expectedNext = "catalog-or-terminal"
		case "close":
			if offset != len(lines) {
				return result, deny("journal_trailing_bytes_after_closed")
			}
		}
	}
	if len(result.records) == 0 || result.records[0]["record"] != "intent" {
		return result, deny("journal_intent_missing")
	}
	if len(result.records) > 4 {
		return result, deny("journal_too_many_records")
	}
	seen := make(map[string]struct{})
	for _, record := range result.records {
		kind := record["record"]
		if _, ok := seen[kind]; ok {
			return result, deny("journal_duplicate_phase")
		}
		seen[kind] = struct{}{}
	}
	if len(result.records) >= 2 && result.records[1]["record"] != "catalog" {
		return result, deny("journal_catalog_phase_missing")
	}
	result.fullSHA256 = digestBytes(data)
	return result, nil
}

func parseRecord(recordType string, lines, schema []string, previousHash string) (map[string]string, []byte, string, error) {
	values := make(map[string]string, len(schema))
	fields := make([]field, 0, len(schema)-1)
	for index, expectedKey := range schema {
		key, value, ok := splitCanonicalLine(lines[index])
		if !ok {
			return nil, nil, "", deny("journal_key_value_line_invalid")
		}
		if key != expectedKey {
			return nil, nil, "", deny("journal_key_missing_duplicate_or_reordered")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, nil, "", deny("journal_duplicate_key")
		}
		values[key] = value
		if key != "record_sha256" {
			fields = append(fields, field{key, value})
		}
	}
	if values["record"] != recordType {
		return nil, nil, "", deny("journal_record_type_mismatch")
	}
	body := canonicalBody(fields)
	expectedHash := digestBytes(body)
	if previousHash != "" {
		expectedHash = chainedDigest(previousHash, body)
	}
	if values["record_sha256"] != expectedHash {
		return nil, nil, "", deny("journal_record_sha256_mismatch")
	}
	return values, body, expectedHash, nil
}

func splitCanonicalLine(line string) (string, string, bool) {
	if strings.Count(line, "=") != 1 {
		return "", "", false
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok || key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func validateRecordValues(recordType string, values map[string]string, prior parsedJournal) error {
	switch recordType {
	case "intent":
		if values["format"] != "client-auth-00043-cleanup-journal-v1" ||
			!hex64RE.MatchString(values["journal_id"]) ||
			requireEpoch(values["created_at_epoch"]) != nil ||
			!runIDRE.MatchString(values["run_id"]) ||
			!hex64RE.MatchString(values["release_manifest_sha256"]) ||
			!hex64RE.MatchString(values["runner_sha256"]) ||
			!hex64RE.MatchString(values["migration_sha256"]) ||
			!sqlIDRE.MatchString(values["database_name"]) ||
			!candidateRE.MatchString(values["candidate"]) ||
			!sqlIDRE.MatchString(values["table"]) ||
			!sqlIDRE.MatchString(values["index_name"]) ||
			!hex64RE.MatchString(values["expected_indexdef_sha256"]) ||
			!hex64RE.MatchString(values["expected_predicate_sha256"]) ||
			!hex64RE.MatchString(values["expected_dependency_sha256"]) ||
			values["attempt"] != "1" || values["status"] != "intent_fsynced" {
			return deny("journal_intent_value_invalid")
		}
		if _, err := parsePositiveUint(values["source_system_identifier"]); err != nil {
			return deny("journal_intent_source_invalid")
		}
		if _, err := parsePositiveUint(values["database_oid"]); err != nil {
			return deny("journal_intent_database_oid_invalid")
		}
	case "catalog":
		if prior.lastType != "intent" ||
			requireEpoch(values["observed_at_epoch"]) != nil ||
			values["journal_id"] != prior.journalID ||
			values["run_id"] != prior.runID ||
			values["candidate"] != prior.candidate ||
			values["constraint_oid"] != "0" ||
			!hex64RE.MatchString(values["catalog_sha256"]) ||
			values["indexdef_sha256"] != prior.indexdefSHA ||
			values["predicate_sha256"] != prior.predicateSHA ||
			values["dependency_sha256"] != prior.dependencySHA ||
			values["classifier"] != "INVALID_EXACT" ||
			values["status"] != "catalog_fsynced" {
			return deny("journal_catalog_value_or_binding_invalid")
		}
		if _, err := parsePositiveUint(values["table_oid"]); err != nil {
			return deny("journal_catalog_table_oid_invalid")
		}
		if _, err := parsePositiveUint(values["index_oid"]); err != nil {
			return deny("journal_catalog_index_oid_invalid")
		}
	case "drop":
		if prior.lastType != "catalog" ||
			requireEpoch(values["dropped_at_epoch"]) != nil ||
			values["journal_id"] != prior.journalID ||
			values["run_id"] != prior.runID ||
			values["candidate"] != prior.candidate ||
			values["index_oid"] != prior.indexOID ||
			values["catalog_sha256"] != prior.catalogSHA ||
			values["classifier"] != "REMOVED_EXACT" ||
			values["status"] != "drop_fsynced" {
			return deny("journal_drop_value_or_binding_invalid")
		}
	case "close":
		if (prior.lastType != "catalog" && prior.lastType != "drop") ||
			requireEpoch(values["closed_at_epoch"]) != nil ||
			values["journal_id"] != prior.journalID ||
			values["run_id"] != prior.runID ||
			values["candidate"] != prior.candidate ||
			values["former_index_oid"] != prior.indexOID ||
			values["classifier"] != "REMOVED_EXACT" ||
			values["status"] != "closed" {
			return deny("journal_close_value_or_binding_invalid")
		}
	default:
		return deny("journal_unknown_record")
	}
	return nil
}

func validateJournalName(basename string, parsed parsedJournal) error {
	match := journalRE.FindStringSubmatch(basename)
	if match == nil || match[1] != parsed.candidate || match[3] != parsed.journalID {
		return deny("journal_basename_identity_mismatch")
	}
	return nil
}
