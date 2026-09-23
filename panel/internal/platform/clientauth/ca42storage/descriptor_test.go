package ca42storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseExactFSVerityInventory(t *testing.T) {
	data, digest := storageFixture(t)
	descriptor, err := Parse(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !descriptor.parsed || len(descriptor.Entries) != AttemptEntryCount+MigrationEntryCount+MinRuntimeEntryCount ||
		descriptor.Entries[AttemptEntryCount+MigrationEntryCount-1].Path != descriptor.AttemptRoot+"/migrations/00042_client_auth_expand.sql" {
		t.Fatalf("unexpected storage descriptor: %+v", descriptor)
	}
	mutated := descriptor
	mutated.Entries = nil
	trusted, err := mutated.VerifiedCopy()
	if err != nil || len(trusted.Entries) != AttemptEntryCount+MigrationEntryCount+MinRuntimeEntryCount {
		t.Fatal("mutable storage projection influenced canonical verification")
	}
}

func TestRuntimeLoaderMustMatchDescriptorArchitecture(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			data, digest := storageFixtureForArchitecture(t, architecture, MinRuntimeEntryCount)
			if _, err := Parse(data, digest); err != nil {
				t.Fatalf("matching runtime loader rejected: %v", err)
			}
			wrongLoader := map[string]string{
				"amd64": "/lib/ld-linux-aarch64.so.1",
				"arm64": "/lib64/ld-linux-x86-64.so.2",
			}[architecture]
			mutated := rebuildInventory(t, data, func(lines []string) {
				for index := range lines {
					if strings.Contains(lines[index], "|runtime_loader|") {
						parts := strings.Split(lines[index], "|")
						parts[4] = hex.EncodeToString([]byte(wrongLoader))
						lines[index] = strings.Join(parts, "|")
						return
					}
				}
			})
			if _, err := Parse(mutated, sha256.Sum256(mutated)); err == nil {
				t.Fatal("foreign-architecture runtime loader accepted")
			}
		})
	}
}

func TestDescriptorV2ExcludesReversePinnedArtifacts(t *testing.T) {
	want := []string{
		"external_manifest", "pathtrust_binary", "attestation_public_key",
		"credential_source_descriptor", "runtime_closure_manifest", "manifest_verifier",
		"preflight_runner", "migration_runner", "goose_binary", "goose_build_info",
		"migration_manifest", "globals_dump", "database_dump",
	}
	got := make([]string, len(attemptRoles))
	forbidden := map[string]bool{
		"trust_capsule": true, "attestation_core": true,
		"attestation": true, "attestation_expected": true,
	}
	for index, role := range attemptRoles {
		got[index] = role.role
		if forbidden[role.role] {
			t.Fatalf("storage descriptor reintroduced content-addressed cycle through %s", role.role)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage descriptor v2 dependency inventory drift: got=%q want=%q", got, want)
	}
	if Format != "pandora-ca42-fsverity-artifact-descriptor-v2" || Profile != "fs-verity-sha256-v2" {
		t.Fatal("acyclic storage descriptor requires the v2 format and profile")
	}
}

func TestBoundDescriptorAccessorsReverifyCanonicalIdentity(t *testing.T) {
	data, digest := storageFixture(t)
	descriptor, err := Parse(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000100, 0).UTC()
	bound := BoundDescriptor{descriptor: descriptor, planSHA: sha256.Sum256([]byte("plan")),
		planNotBefore: time.Unix(1700000000, 0).UTC(), planNotAfter: time.Unix(1700003600, 0).UTC(), bound: true}
	count, err := bound.EntryCountAt(now)
	wantCount := AttemptEntryCount + MigrationEntryCount + MinRuntimeEntryCount
	if err != nil || count != wantCount {
		t.Fatalf("entry count got=%d err=%v want=%d", count, err, wantCount)
	}
	for index := 0; index < count; index++ {
		entry, err := bound.EntryAt(index, now)
		if err != nil {
			t.Fatalf("entry %d: %v", index, err)
		}
		snapshot, err := entry.SnapshotAt(now)
		if err != nil || snapshot.Ordinal != uint64(index+1) {
			t.Fatalf("entry %d snapshot=%+v err=%v", index, snapshot, err)
		}
		lookedUp, err := bound.LookupPath(snapshot.Path, now)
		if err != nil {
			t.Fatalf("lookup %q: %v", snapshot.Path, err)
		}
		lookupSnapshot, err := lookedUp.SnapshotAt(now)
		if err != nil || lookupSnapshot.Ordinal != snapshot.Ordinal {
			t.Fatalf("lookup identity mismatch: %+v err=%v", lookupSnapshot, err)
		}
	}
	if _, err := bound.EntryAt(-1, now); err == nil {
		t.Fatal("negative bound entry index accepted")
	}
	if _, err := bound.EntryAt(count, now); err == nil {
		t.Fatal("past-end bound entry index accepted")
	}
	for _, value := range []string{"", "relative", "/tmp/../tmp/x", "/tmp//x", "/tmp/\x00x", "C:\\tmp\\x"} {
		if _, err := bound.LookupPath(value, now); err == nil {
			t.Fatalf("noncanonical lookup path accepted: %q", value)
		}
	}
	if _, err := bound.LookupPath("/missing", now); err == nil {
		t.Fatal("missing lookup path accepted")
	}

	first, err := bound.EntryAt(0, now)
	if err != nil {
		t.Fatal(err)
	}
	original, err := first.SnapshotAt(now)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Entries[0].Path = "/attacker"
	original.Path = "/caller-mutation"
	again, err := first.SnapshotAt(now)
	if err != nil || again.Path == descriptor.Entries[0].Path || again.Path == original.Path {
		t.Fatalf("mutable projection influenced bound entry: %+v err=%v", again, err)
	}
}

func TestBoundDescriptorAccessorsRejectZeroAndIdentityMutation(t *testing.T) {
	now := time.Unix(1700000100, 0).UTC()
	if _, err := (BoundDescriptor{}).EntryCountAt(now); err == nil {
		t.Fatal("zero bound descriptor accepted")
	}
	if _, err := (BoundDescriptor{}).LookupPath("/tmp/x", now); err == nil {
		t.Fatal("zero bound descriptor path lookup accepted")
	}
	if _, err := (BoundEntry{}).SnapshotAt(now); err == nil {
		t.Fatal("zero bound entry accepted")
	}
	data, digest := storageFixture(t)
	descriptor, err := Parse(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	bound := BoundDescriptor{descriptor: descriptor, planSHA: sha256.Sum256([]byte("plan")),
		planNotBefore: time.Unix(1700000000, 0).UTC(), planNotAfter: time.Unix(1700003600, 0).UTC(), bound: true}
	bound.descriptor.SHA256[0] ^= 0xff
	if _, err := bound.EntryCountAt(now); err == nil {
		t.Fatal("mutated bound descriptor identity accepted")
	}
	valid := BoundDescriptor{descriptor: descriptor, planSHA: sha256.Sum256([]byte("plan")),
		planNotBefore: time.Unix(1700000000, 0).UTC(), planNotAfter: time.Unix(1700003600, 0).UTC(), bound: true}
	if _, err := valid.EntryCountAt(time.Unix(1700003600, 0).UTC()); err == nil {
		t.Fatal("expired bound descriptor accepted")
	}
}

func TestParseRejectsInventoryDriftAndAliases(t *testing.T) {
	base, _ := storageFixture(t)
	tests := map[string]func([]byte) []byte{
		"crlf":      func(value []byte) []byte { return []byte(strings.ReplaceAll(string(value), "\n", "\r\n")) },
		"double_lf": func(value []byte) []byte { return append(value, '\n') },
		"readonly_fallback": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), Profile, "readonly-bind-v1", 1))
		},
		"inventory_hash_stale": func(value []byte) []byte { return []byte(strings.Replace(string(value), "|0400|", "|0440|", 1)) },
		"migration_43": func(value []byte) []byte {
			return rebuildInventory(t, value, func(lines []string) {
				index := headerLineCount + AttemptEntryCount + MigrationEntryCount - 1
				lines[index] = strings.Replace(lines[index], hex.EncodeToString([]byte("/run/pandora/ca42/attempt-1/migrations/00042_client_auth_expand.sql")), hex.EncodeToString([]byte("/run/pandora/ca42/attempt-1/migrations/00043_client_auth_expand.sql")), 1)
			})
		},
		"duplicate_inode": func(value []byte) []byte {
			return rebuildInventory(t, value, func(lines []string) {
				first := strings.Split(lines[headerLineCount], "|")
				second := strings.Split(lines[headerLineCount+1], "|")
				second[11] = first[11]
				lines[headerLineCount+1] = strings.Join(second, "|")
			})
		},
		"no_loader": func(value []byte) []byte {
			return rebuildInventory(t, value, func(lines []string) {
				index := headerLineCount + AttemptEntryCount + MigrationEntryCount + 2
				lines[index] = strings.Replace(lines[index], "|runtime_loader|", "|runtime_library|", 1)
			})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(append([]byte(nil), base...))
			if _, err := Parse(candidate, sha256.Sum256(candidate)); err == nil {
				t.Fatal("invalid fs-verity inventory accepted")
			}
		})
	}
}

func storageFixture(t *testing.T) ([]byte, [sha256.Size]byte) {
	return storageFixtureWithRuntime(t, MinRuntimeEntryCount)
}

func storageFixtureWithRuntime(t *testing.T, runtimeCount int) ([]byte, [sha256.Size]byte) {
	return storageFixtureForArchitecture(t, "amd64", runtimeCount)
}

func storageFixtureForArchitecture(t *testing.T, architecture string, runtimeCount int) ([]byte, [sha256.Size]byte) {
	t.Helper()
	if architecture != "amd64" && architecture != "arm64" {
		t.Fatalf("invalid fixture architecture: %s", architecture)
	}
	if runtimeCount < MinRuntimeEntryCount || runtimeCount > MaxRuntimeEntryCount {
		t.Fatalf("invalid fixture runtime count: %d", runtimeCount)
	}
	root := "/run/pandora/ca42/attempt-1"
	entries := make([]string, 0, AttemptEntryCount+MigrationEntryCount+runtimeCount)
	add := func(scope, role, kind, path, mode string) {
		ordinal := len(entries) + 1
		content := sha256.Sum256([]byte("content:" + strconv.Itoa(ordinal) + ":" + role))
		verity := sha256.Sum256([]byte("verity:" + strconv.Itoa(ordinal) + ":" + role))
		entries = append(entries, fmt.Sprintf("entry=%06d|%s|%s|%s|%s|%s|0|0|1|%d|10|%d|20|%x|%x",
			ordinal, scope, role, kind, hex.EncodeToString([]byte(path)), mode, 1000+ordinal, 2000+ordinal, content, verity))
	}
	for _, expected := range attemptRoles {
		add("attempt", expected.role, expected.kind, root+"/"+expected.name, expected.mode)
	}
	for _, name := range migrationNames {
		add("migration", "migration_sql", "sql", root+"/migrations/"+name, "0400")
	}
	add("system", "bash", "elf", "/usr/bin/bash", "0755")
	add("system", "docker", "elf", "/usr/bin/docker", "0755")
	loaderPath := map[string]string{"amd64": "/lib64/ld-linux-x86-64.so.2", "arm64": "/lib/ld-linux-aarch64.so.1"}[architecture]
	add("system", "runtime_loader", "elf", loaderPath, "0755")
	add("system", "runtime_library", "elf", "/usr/lib/libc.so.6", "0644")
	for index := MinRuntimeEntryCount; index < runtimeCount; index++ {
		add("system", "runtime_library", "elf", fmt.Sprintf("/usr/lib/pandora-lib-%03d.so", index), "0644")
	}
	inventory := strings.Join(entries, "\n") + "\n"
	inventoryDigest := sha256.Sum256([]byte(inventory))
	headers := []string{
		"format=" + Format, "profile=" + Profile, "release_id=release-1", "release_run_id=run-1", "attempt_id=attempt-1",
		"architecture=" + architecture, "attempt_root_hex=" + hex.EncodeToString([]byte(root)), "content_hash_algorithm=sha256",
		"verity_hash_algorithm=sha256", "attempt_entry_count=" + strconv.Itoa(AttemptEntryCount), "migration_entry_count=42", "runtime_entry_count=" + strconv.Itoa(runtimeCount),
		"entry_count=" + strconv.Itoa(AttemptEntryCount+MigrationEntryCount+runtimeCount), fmt.Sprintf("inventory_sha256=%x", inventoryDigest),
	}
	data := []byte(strings.Join(headers, "\n") + "\n" + inventory)
	return data, sha256.Sum256(data)
}

func rebuildInventory(t *testing.T, data []byte, mutate func([]string)) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	mutate(lines)
	inventory := strings.Join(lines[headerLineCount:], "\n") + "\n"
	digest := sha256.Sum256([]byte(inventory))
	lines[headerLineCount-1] = fmt.Sprintf("inventory_sha256=%x", digest)
	return []byte(strings.Join(lines, "\n") + "\n")
}
