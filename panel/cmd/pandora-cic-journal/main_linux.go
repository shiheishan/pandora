//go:build linux && (amd64 || arm64)

// [INPUT]: 只依赖 Go 标准库（mock 测试禁止 os/exec 与第三方导入），依赖同包 journal_linux.go / fs_linux.go / parse_linux.go 与 syscall_linux_{amd64,arm64}.go 的系统调用号
// [OUTPUT]: 对外提供 可执行入口 pandora-cic-journal：create-intent / append-catalog / append-drop / append-close 四个子命令
// [POS]: pandora-cic-journal 的命令行与共用模型：退出码、系统调用常量、可替换的故障注入点（journalRename 等）、参数解析与路径白名单、规范化记录体与哈希链摘要；发布与追加在 journal_linux.go，可信文件系统原语在 fs_linux.go，解析校验在 parse_linux.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
