package ca42storage

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
)

const (
	Format                = "pandora-ca42-fsverity-artifact-descriptor-v2"
	Profile               = "fs-verity-sha256-v2"
	AttemptEntryCount     = 13
	MigrationEntryCount   = 42
	MinRuntimeEntryCount  = 4
	MaxRuntimeEntryCount  = 130
	MaxDescriptorBytes    = 4 << 20
	MaxOrdinaryEntryBytes = uint64(1 << 30)
	MaxDatabaseDumpBytes  = uint64(1 << 40)
	headerLineCount       = 14
)

var (
	hex64     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeToken = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

var headerNames = [...]string{
	"format", "profile", "release_id", "release_run_id", "attempt_id", "architecture", "attempt_root_hex",
	"content_hash_algorithm", "verity_hash_algorithm", "attempt_entry_count", "migration_entry_count",
	"runtime_entry_count", "entry_count", "inventory_sha256",
}

var attemptRoles = [...]struct {
	role, kind, name, mode string
}{
	// The descriptor intentionally excludes trust_capsule, attestation_core,
	// attestation, and attestation_expected. Those artifacts pin this
	// descriptor's SHA-256, so inventorying their content hashes here would
	// create an unsatisfiable content-addressed cycle.
	{"external_manifest", "manifest", "external-manifest.json", "0400"},
	{"pathtrust_binary", "executable", "pathtrust", "0500"},
	{"attestation_public_key", "data", "attestation-public-key", "0400"},
	{"credential_source_descriptor", "data", "credential-source.descriptor", "0400"},
	{"runtime_closure_manifest", "manifest", "runtime-closure.manifest", "0400"},
	{"manifest_verifier", "executable", "manifest-verifier", "0500"},
	{"preflight_runner", "executable", "preflight-runner", "0500"},
	{"migration_runner", "executable", "migration-runner", "0500"},
	{"goose_binary", "executable", "goose", "0500"},
	{"goose_build_info", "manifest", "goose-build-info.manifest", "0400"},
	{"migration_manifest", "manifest", "migration-set.manifest", "0400"},
	{"globals_dump", "dump", "globals.dump", "0400"},
	{"database_dump", "dump", "database.dump", "0400"},
}

var migrationNames = [...]string{
	"00001_foundation.sql", "00002_identity.sql", "00003_catalog_subscription.sql", "00004_billing_ledger.sql",
	"00005_node_fabric.sql", "00006_metering.sql", "00007_client_delivery.sql", "00008_ops_marketing.sql",
	"00009_security_audit.sql", "00010_seed_rbac.sql", "00011_app_role.sql", "00012_audit_node_actor.sql",
	"00013_xboard_node_protocol.sql", "00014_subscription_proxy_uuid.sql", "00015_node_metrics.sql", "00016_node_kernel.sql",
	"00017_node_routing.sql", "00018_subscription_delivery.sql", "00019_subscription_token_vault.sql", "00020_change_notify.sql",
	"00021_notify_fix_tables.sql", "00022_notify_exclude_messages.sql", "00023_notification_seed.sql", "00024_device_limit_modes.sql",
	"00025_audit_ip_vault.sql", "00026_ip_cluster_window.sql", "00027_drop_cluster_idx.sql", "00028_commission_settings.sql",
	"00029_commission_defaults.sql", "00030_mail_settings.sql", "00031_pandora_brand.sql", "00032_revenue_report_adjustments.sql",
	"00033_server_node_split.sql", "00034_node_admin_concurrency.sql", "00035_catalog_authoring.sql", "00036_order_reservations.sql",
	"00037_idempotency_runtime_hardening.sql", "00038_idempotency_resource_binding.sql", "00039_bound_idempotency_success.sql",
	"00040_order_release_and_late_suspense.sql", "00041_dashboard_read_models.sql", "00042_client_auth_expand.sql",
}

type Entry struct {
	Ordinal                       uint64
	Scope, Role, Kind, Path, Mode string
	UID, GID, NLink, Size         uint64
	Device, Inode, MountID        uint64
	ContentSHA256, VeritySHA256   string
}

type Descriptor struct {
	ReleaseID, ReleaseRunID, AttemptID, Architecture string
	AttemptRoot                                      string
	RuntimeEntryCount                                uint64
	InventorySHA256                                  string
	Entries                                          []Entry
	SHA256                                           [sha256.Size]byte
	parsed                                           bool
	canonical                                        []byte
}

func (descriptor Descriptor) IsParsed() bool {
	_, err := descriptor.VerifiedCopy()
	return err == nil
}

// Parse checks canonical inventory structure. Kernel fs-verity measurement is
// deliberately a separate Linux-only gate and may not be replaced by this text.
func Parse(data []byte, expectedSHA256 [sha256.Size]byte) (Descriptor, error) {
	var empty Descriptor
	if len(data) == 0 || len(data) > MaxDescriptorBytes || data[len(data)-1] != '\n' ||
		bytes.HasSuffix(data, []byte("\n\n")) || bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("artifact storage descriptor envelope invalid")
	}
	digest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("artifact storage descriptor identity mismatch")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) < headerLineCount+AttemptEntryCount+MigrationEntryCount+MinRuntimeEntryCount {
		return empty, errors.New("artifact storage descriptor too few fields")
	}
	values := make(map[string]string, len(headerNames))
	for index, name := range headerNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("artifact storage descriptor header order invalid: %s", name)
		}
		values[name] = string(lines[index][len(prefix):])
	}
	if values["format"] != Format || values["profile"] != Profile || values["content_hash_algorithm"] != "sha256" ||
		values["verity_hash_algorithm"] != "sha256" || values["attempt_entry_count"] != strconv.Itoa(AttemptEntryCount) ||
		values["migration_entry_count"] != strconv.Itoa(MigrationEntryCount) || !safeToken.MatchString(values["release_id"]) ||
		!safeToken.MatchString(values["release_run_id"]) || !safeToken.MatchString(values["attempt_id"]) ||
		(values["architecture"] != "amd64" && values["architecture"] != "arm64") {
		return empty, errors.New("artifact storage descriptor fixed header invalid")
	}
	attemptRoot, err := decodeCanonicalPath(values["attempt_root_hex"])
	if err != nil || attemptRoot != "/run/pandora/ca42/"+values["attempt_id"] {
		return empty, errors.New("artifact storage descriptor attempt root invalid")
	}
	runtimeCount, err := positiveUint(values["runtime_entry_count"], MaxRuntimeEntryCount)
	if err != nil || runtimeCount < MinRuntimeEntryCount {
		return empty, errors.New("artifact storage descriptor runtime count invalid")
	}
	total := uint64(AttemptEntryCount+MigrationEntryCount) + runtimeCount
	if values["entry_count"] != strconv.FormatUint(total, 10) || uint64(len(lines)-headerLineCount) != total || !nonZeroHex64(values["inventory_sha256"]) {
		return empty, errors.New("artifact storage descriptor entry count invalid")
	}
	inventory := append(bytes.Join(lines[headerLineCount:], []byte{'\n'}), '\n')
	inventoryDigest := sha256.Sum256(inventory)
	if values["inventory_sha256"] != hex.EncodeToString(inventoryDigest[:]) {
		return empty, errors.New("artifact storage descriptor inventory identity mismatch")
	}
	entries := make([]Entry, 0, total)
	seenPaths, seenInodes := map[string]bool{}, map[string]bool{}
	for index, line := range lines[headerLineCount:] {
		entry, err := parseEntry(line, uint64(index+1))
		if err != nil {
			return empty, fmt.Errorf("artifact storage descriptor entry %d: %w", index+1, err)
		}
		inodeKey := fmt.Sprintf("%d:%d", entry.Device, entry.Inode)
		if seenPaths[entry.Path] || seenInodes[inodeKey] {
			return empty, errors.New("artifact storage descriptor duplicate file identity")
		}
		seenPaths[entry.Path], seenInodes[inodeKey] = true, true
		entries = append(entries, entry)
	}
	if err := validateInventory(entries, attemptRoot, int(runtimeCount), values["architecture"]); err != nil {
		return empty, err
	}
	return Descriptor{
		ReleaseID: values["release_id"], ReleaseRunID: values["release_run_id"], AttemptID: values["attempt_id"],
		Architecture: values["architecture"], AttemptRoot: attemptRoot, RuntimeEntryCount: runtimeCount,
		InventorySHA256: values["inventory_sha256"], Entries: entries, SHA256: digest, parsed: true,
		canonical: append([]byte(nil), data...),
	}, nil
}

type BoundDescriptor struct {
	descriptor                  Descriptor
	planSHA                     [sha256.Size]byte
	planNotBefore, planNotAfter time.Time
	bound                       bool
}

type BoundEntry struct {
	entry                       Entry
	descriptorSHA, planSHA      [sha256.Size]byte
	planNotBefore, planNotAfter time.Time
	seal                        [sha256.Size]byte
	index                       int
	bound                       bool
}

func BindPlan(descriptor Descriptor, plan ca42executionv2.Plan, now time.Time) (BoundDescriptor, error) {
	trustedDescriptor, err := descriptor.VerifiedCopy()
	if err != nil {
		return BoundDescriptor{}, err
	}
	trustedPlan, err := plan.VerifiedCopyAt(now)
	if err != nil {
		return BoundDescriptor{}, err
	}
	descriptor, plan = trustedDescriptor, trustedPlan
	if hex.EncodeToString(descriptor.SHA256[:]) != plan.ArtifactStorageDescriptorSHA256 ||
		descriptor.ReleaseID != plan.ReleaseID || descriptor.ReleaseRunID != plan.ReleaseRunID ||
		descriptor.AttemptID != plan.AttemptID || descriptor.Architecture != plan.Architecture ||
		plan.ArtifactStorageProfile != Profile {
		return BoundDescriptor{}, errors.New("artifact storage descriptor plan identity mismatch")
	}
	byRole := make(map[string]Entry, AttemptEntryCount)
	for _, entry := range descriptor.Entries[:AttemptEntryCount] {
		byRole[entry.Role] = entry
	}
	checks := map[string]string{
		"pathtrust_binary":       plan.PathtrustBinarySHA256,
		"attestation_public_key": plan.AttestationPublicKeySHA256,
		"external_manifest":      plan.ExternalManifestSHA256, "credential_source_descriptor": plan.CredentialSourceDescriptorSHA256,
		"runtime_closure_manifest": plan.RuntimeClosureManifestSHA256, "manifest_verifier": plan.ManifestVerifierSHA256,
		"preflight_runner": plan.PreflightRunnerSHA256, "migration_runner": plan.MigrationRunnerSHA256,
		"goose_binary": plan.GooseBinarySHA256, "goose_build_info": plan.GooseBuildInfoSHA256,
		"migration_manifest": plan.MigrationSetSHA256, "globals_dump": plan.GlobalsDumpSHA256, "database_dump": plan.DatabaseDumpSHA256,
	}
	for role, expected := range checks {
		if byRole[role].ContentSHA256 != expected {
			return BoundDescriptor{}, fmt.Errorf("artifact storage descriptor plan hash mismatch: %s", role)
		}
	}
	if byRole["globals_dump"].Size != plan.GlobalsDumpSizeBytes || byRole["database_dump"].Size != plan.DatabaseDumpSizeBytes ||
		byRole["pathtrust_binary"].Device != plan.PathtrustDevice ||
		descriptor.Entries[AttemptEntryCount+MigrationEntryCount-1].ContentSHA256 != plan.ClientAuth00042SHA256 ||
		descriptor.Entries[AttemptEntryCount+MigrationEntryCount].ContentSHA256 != plan.BashBinarySHA256 ||
		descriptor.Entries[AttemptEntryCount+MigrationEntryCount].Device != plan.BashDevice ||
		descriptor.Entries[AttemptEntryCount+MigrationEntryCount+1].ContentSHA256 != plan.DockerClientSHA256 ||
		descriptor.Entries[AttemptEntryCount+MigrationEntryCount+1].Device != plan.DockerClientDevice {
		return BoundDescriptor{}, errors.New("artifact storage descriptor plan size or system hash mismatch")
	}
	return BoundDescriptor{descriptor: descriptor, planSHA: plan.SHA256,
		planNotBefore: plan.NotBefore.UTC(), planNotAfter: plan.NotAfter.UTC(), bound: true}, nil
}

func (descriptor Descriptor) VerifiedCopy() (Descriptor, error) {
	if !descriptor.parsed || len(descriptor.canonical) == 0 {
		return Descriptor{}, errors.New("artifact storage descriptor parsed capability invalid")
	}
	digest := sha256.Sum256(descriptor.canonical)
	if digest != descriptor.SHA256 {
		return Descriptor{}, errors.New("artifact storage descriptor projection identity changed")
	}
	return Parse(descriptor.canonical, digest)
}

func (bound BoundDescriptor) verifiedCopyAt(now time.Time) (BoundDescriptor, error) {
	if !bound.bound || bound.planSHA == ([sha256.Size]byte{}) || now.IsZero() ||
		bound.planNotBefore.IsZero() || bound.planNotAfter.IsZero() ||
		now.Before(bound.planNotBefore) || !now.Before(bound.planNotAfter) {
		return BoundDescriptor{}, errors.New("bound artifact descriptor capability invalid")
	}
	descriptor, err := bound.descriptor.VerifiedCopy()
	if err != nil {
		return BoundDescriptor{}, errors.New("bound artifact descriptor identity invalid")
	}
	return BoundDescriptor{descriptor: descriptor, planSHA: bound.planSHA,
		planNotBefore: bound.planNotBefore, planNotAfter: bound.planNotAfter, bound: true}, nil
}

// EntryCount returns the exact number of canonical inventory entries after
// re-verifying the descriptor. It conveys no execution authority.
func (bound BoundDescriptor) EntryCountAt(now time.Time) (int, error) {
	trusted, err := bound.verifiedCopyAt(now)
	if err != nil {
		return 0, err
	}
	return len(trusted.descriptor.Entries), nil
}

// EntryAt returns an opaque structural capability for one canonical entry.
func (bound BoundDescriptor) EntryAt(index int, now time.Time) (BoundEntry, error) {
	trusted, err := bound.verifiedCopyAt(now)
	if err != nil || index < 0 || index >= len(trusted.descriptor.Entries) {
		return BoundEntry{}, errors.New("bound artifact entry index invalid")
	}
	return trusted.cachedEntry(index), nil
}

// LookupPath resolves an exact canonical absolute path. It never opens the
// path and therefore cannot replace retained-FD or path-chain verification.
func (bound BoundDescriptor) LookupPath(canonicalPath string, now time.Time) (BoundEntry, error) {
	if decoded, err := decodeCanonicalPath(hex.EncodeToString([]byte(canonicalPath))); err != nil || decoded != canonicalPath {
		return BoundEntry{}, errors.New("bound artifact lookup path invalid")
	}
	trusted, err := bound.verifiedCopyAt(now)
	if err != nil {
		return BoundEntry{}, err
	}
	for index, entry := range trusted.descriptor.Entries {
		if entry.Path == canonicalPath {
			return trusted.cachedEntry(index), nil
		}
	}
	return BoundEntry{}, errors.New("bound artifact lookup path not found")
}

func (bound BoundEntry) verifiedEntryAt(now time.Time) (Entry, error) {
	if !bound.bound || bound.index < 0 || now.IsZero() || bound.entry.Ordinal != uint64(bound.index+1) ||
		bound.descriptorSHA == ([sha256.Size]byte{}) || bound.planSHA == ([sha256.Size]byte{}) ||
		bound.planNotBefore.IsZero() || bound.planNotAfter.IsZero() || now.Before(bound.planNotBefore) || !now.Before(bound.planNotAfter) ||
		bound.seal != sealBoundEntry(bound.entry, bound.index, bound.descriptorSHA, bound.planSHA, bound.planNotBefore, bound.planNotAfter) {
		return Entry{}, errors.New("bound artifact entry capability invalid")
	}
	return bound.entry, nil
}

// cachedEntriesAt verifies the canonical descriptor once and creates sealed,
// immutable entry capabilities for an exact inventory pass. It is internal so
// callers cannot manufacture or mutate the snapshot boundary.
func (bound BoundDescriptor) cachedEntriesAt(now time.Time) (BoundDescriptor, []BoundEntry, error) {
	trusted, err := bound.verifiedCopyAt(now)
	if err != nil {
		return BoundDescriptor{}, nil, err
	}
	entries := make([]BoundEntry, len(trusted.descriptor.Entries))
	for index := range entries {
		entries[index] = trusted.cachedEntry(index)
	}
	return trusted, entries, nil
}

func (bound BoundDescriptor) cachedEntry(index int) BoundEntry {
	entry := bound.descriptor.Entries[index]
	result := BoundEntry{entry: entry, descriptorSHA: bound.descriptor.SHA256, planSHA: bound.planSHA,
		planNotBefore: bound.planNotBefore, planNotAfter: bound.planNotAfter, index: index, bound: true}
	result.seal = sealBoundEntry(entry, index, result.descriptorSHA, result.planSHA, result.planNotBefore, result.planNotAfter)
	return result
}

func sealBoundEntry(entry Entry, index int, descriptorSHA, planSHA [sha256.Size]byte, notBefore, notAfter time.Time) [sha256.Size]byte {
	value := fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s\x00%s\x00%x\x00%x\x00%d\x00%d",
		index, entry.Ordinal, entry.Scope, entry.Role, entry.Kind, entry.Path, entry.Mode, entry.UID, entry.GID, entry.NLink,
		entry.Size, entry.Device, entry.Inode, entry.MountID, entry.ContentSHA256, entry.VeritySHA256,
		descriptorSHA, planSHA, notBefore.UnixNano(), notAfter.UnixNano())
	return sha256.Sum256([]byte(value))
}

// SnapshotAt returns a scalar diagnostic copy after seal and window checks.
// It is not a retained-FD, fs-verity, execution, or admission capability.
func (bound BoundEntry) SnapshotAt(now time.Time) (Entry, error) {
	return bound.verifiedEntryAt(now)
}

func parseEntry(line []byte, ordinal uint64) (Entry, error) {
	var empty Entry
	prefix := []byte("entry=")
	if !bytes.HasPrefix(line, prefix) {
		return empty, errors.New("entry prefix invalid")
	}
	parts := strings.Split(string(line[len(prefix):]), "|")
	if len(parts) != 15 || parts[0] != fmt.Sprintf("%06d", ordinal) {
		return empty, errors.New("entry width or ordinal invalid")
	}
	pathValue, err := decodeCanonicalPath(parts[4])
	if err != nil {
		return empty, errors.New("entry path invalid")
	}
	uid, err1 := canonicalUint(parts[6], 1<<32-1)
	gid, err2 := canonicalUint(parts[7], 1<<32-1)
	nlink, err3 := positiveUint(parts[8], 1)
	sizeLimit := MaxOrdinaryEntryBytes
	if parts[2] == "database_dump" {
		sizeLimit = MaxDatabaseDumpBytes
	}
	size, err4 := positiveUint(parts[9], sizeLimit)
	device, err5 := positiveUint(parts[10], ^uint64(0))
	inode, err6 := positiveUint(parts[11], ^uint64(0))
	mountID, err7 := positiveUint(parts[12], ^uint64(0))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil || err7 != nil ||
		uid != 0 || gid != 0 || !nonZeroHex64(parts[13]) || !nonZeroHex64(parts[14]) {
		return empty, errors.New("entry metadata invalid")
	}
	return Entry{Ordinal: ordinal, Scope: parts[1], Role: parts[2], Kind: parts[3], Path: pathValue, Mode: parts[5],
		UID: uid, GID: gid, NLink: nlink, Size: size, Device: device, Inode: inode, MountID: mountID,
		ContentSHA256: parts[13], VeritySHA256: parts[14]}, nil
}

func validateInventory(entries []Entry, root string, runtimeCount int, architecture string) error {
	for index, expected := range attemptRoles {
		entry := entries[index]
		if entry.Scope != "attempt" || entry.Role != expected.role || entry.Kind != expected.kind || entry.Mode != expected.mode ||
			entry.Path != root+"/"+expected.name {
			return fmt.Errorf("artifact storage descriptor attempt role %d invalid", index+1)
		}
	}
	for index, name := range migrationNames {
		entry := entries[AttemptEntryCount+index]
		if entry.Scope != "migration" || entry.Role != "migration_sql" || entry.Kind != "sql" || entry.Mode != "0400" ||
			entry.Path != root+"/migrations/"+name {
			return fmt.Errorf("artifact storage descriptor migration %d invalid", index+1)
		}
	}
	runtime := entries[AttemptEntryCount+MigrationEntryCount:]
	if len(runtime) != runtimeCount || runtime[0].Scope != "system" || runtime[0].Role != "bash" || runtime[0].Kind != "elf" || runtime[0].Path != "/usr/bin/bash" || runtime[0].Mode != "0755" ||
		runtime[1].Scope != "system" || runtime[1].Role != "docker" || runtime[1].Kind != "elf" || runtime[1].Path != "/usr/bin/docker" || runtime[1].Mode != "0755" {
		return errors.New("artifact storage descriptor fixed system entries invalid")
	}
	seenLibrary, loaderCount, libraryCount, previous := false, 0, 0, ""
	for _, entry := range runtime[2:] {
		if entry.Scope != "system" || entry.Kind != "elf" || (entry.Mode != "0755" && entry.Mode != "0644") || !systemLibraryPath(entry.Path) {
			return errors.New("artifact storage descriptor runtime closure entry invalid")
		}
		switch entry.Role {
		case "runtime_loader":
			if seenLibrary || entry.Mode != "0755" || !runtimeLoaderMatchesArchitecture(entry.Path, architecture) {
				return errors.New("artifact storage descriptor runtime loader ordering invalid")
			}
			loaderCount++
		case "runtime_library":
			seenLibrary = true
			libraryCount++
		default:
			return errors.New("artifact storage descriptor runtime role invalid")
		}
		if previous != "" && entry.Path <= previous {
			return errors.New("artifact storage descriptor runtime path ordering invalid")
		}
		previous = entry.Path
	}
	if loaderCount == 0 || libraryCount == 0 {
		return errors.New("artifact storage descriptor runtime closure incomplete")
	}
	return nil
}

func runtimeLoaderMatchesArchitecture(loaderPath, architecture string) bool {
	base := pathpkg.Base(loaderPath)
	switch architecture {
	case "amd64":
		return base == "ld-linux-x86-64.so.2"
	case "arm64":
		return base == "ld-linux-aarch64.so.1"
	default:
		return false
	}
}

func decodeCanonicalPath(value string) (string, error) {
	if value == "" || len(value)%2 != 0 || strings.ToLower(value) != value {
		return "", errors.New("path encoding invalid")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) == 0 || decoded[0] != '/' || bytes.IndexByte(decoded, 0) >= 0 {
		return "", errors.New("path encoding invalid")
	}
	for _, value := range decoded {
		if value < 0x21 || value > 0x7e || value == '|' || value == '\\' {
			return "", errors.New("path byte invalid")
		}
	}
	pathValue := string(decoded)
	if pathpkg.Clean(pathValue) != pathValue || strings.Contains(pathValue, "//") {
		return "", errors.New("path not canonical")
	}
	return pathValue, nil
}

func systemLibraryPath(value string) bool {
	return strings.HasPrefix(value, "/lib/") || strings.HasPrefix(value, "/lib64/") ||
		strings.HasPrefix(value, "/usr/lib/") || strings.HasPrefix(value, "/usr/lib64/")
}

func nonZeroHex64(value string) bool {
	return hex64.MatchString(value) && value != strings.Repeat("0", 64)
}

func canonicalUint(value string, maximum uint64) (uint64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("integer invalid")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed > maximum || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("integer invalid")
	}
	return parsed, nil
}

func positiveUint(value string, maximum uint64) (uint64, error) {
	parsed, err := canonicalUint(value, maximum)
	if err != nil || parsed == 0 {
		return 0, errors.New("positive integer invalid")
	}
	return parsed, nil
}
