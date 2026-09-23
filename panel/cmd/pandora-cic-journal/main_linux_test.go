//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func resetJournalHooks() {
	journalWrite = syscall.Write
	journalFdatasync = syscall.Fdatasync
	journalFsync = syscall.Fsync
	journalRename = renameAt2NoReplace
}

func rootJournalDirs(t *testing.T) (string, int, syscall.Stat_t, int, syscall.Stat_t) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root required for journal metadata contract")
	}
	base, err := os.MkdirTemp("/root", "pandora-cic-journal-test.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		resetJournalHooks()
		_ = os.RemoveAll(base)
	})
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(base, "journal")
	if err := os.Mkdir(journalPath, 0700); err != nil {
		t.Fatal(err)
	}
	runFD, err := syscall.Open(base, syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(runFD) })
	journalFD, err := syscall.Open(journalPath, syscall.O_RDONLY|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(journalFD) })
	var runStat, journalStat syscall.Stat_t
	if err := syscall.Fstat(runFD, &runStat); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Fstat(journalFD, &journalStat); err != nil {
		t.Fatal(err)
	}
	return base, runFD, runStat, journalFD, journalStat
}

func testIntentRecord() []byte {
	h1 := "1111111111111111111111111111111111111111111111111111111111111111"
	h2 := "2222222222222222222222222222222222222222222222222222222222222222"
	h3 := "3333333333333333333333333333333333333333333333333333333333333333"
	body := canonicalBody([]field{
		{"format", "client-auth-00043-cleanup-journal-v1"},
		{"record", "intent"},
		{"journal_id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"created_at_epoch", "1"},
		{"run_id", "run-00043"},
		{"release_manifest_sha256", h1},
		{"runner_sha256", h2},
		{"migration_sha256", h3},
		{"source_system_identifier", "100"},
		{"database_name", "aegis"},
		{"database_oid", "200"},
		{"candidate", "U43-01"},
		{"table", "users"},
		{"index_name", "uq_users_email"},
		{"expected_indexdef_sha256", h1},
		{"expected_predicate_sha256", h2},
		{"expected_dependency_sha256", h3},
		{"attempt", "1"},
		{"status", "intent_fsynced"},
	})
	return appendRecordHash(body, digestBytes(body))
}

func TestUnpublishedFaultsNeverCreateJournalSegment(t *testing.T) {
	base, runFD, runStat, _, _ := rootJournalDirs(t)
	content := testIntentRecord()
	tests := []struct {
		name   string
		inject func()
	}{
		{"short-write", func() {
			calls := 0
			journalWrite = func(fd int, data []byte) (int, error) {
				calls++
				if calls == 1 {
					return 1, nil
				}
				return 0, syscall.ENOSPC
			}
		}},
		{"enospc", func() {
			journalWrite = func(int, []byte) (int, error) { return 0, syscall.ENOSPC }
		}},
		{"fdatasync", func() {
			journalFdatasync = func(int) error { return syscall.EIO }
		}},
		{"file-fsync", func() {
			journalFsync = func(int) error { return syscall.EIO }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetJournalHooks()
			test.inject()
			err := writeImmutableRecordAt(runFD, uint64(runStat.Dev), "."+test.name+".stage", content)
			if err == nil {
				t.Fatal("fault unexpectedly succeeded")
			}
			entries, err := os.ReadDir(filepath.Join(base, "journal"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("unpublished failure exposed %d journal segments", len(entries))
			}
		})
	}
}

func TestKillWindowsLeavePublishedRecordAbsentOrComplete(t *testing.T) {
	base, runFD, runStat, journalFD, journalStat := rootJournalDirs(t)
	content := testIntentRecord()
	stage := ".kill.stage"
	if err := writeImmutableRecordAt(runFD, uint64(runStat.Dev), stage, content); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "journal", "000.intent.record")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-rename kill window exposed record: %v", err)
	}
	if err := renameAt2NoReplace(runFD, stage, journalFD, "000.intent.record"); err != nil {
		t.Fatal(err)
	}
	// Simulate kill immediately after rename and before directory fsync.
	got, err := os.ReadFile(filepath.Join(base, "journal", "000.intent.record"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("post-rename record is not complete")
	}
	if err := syscall.Fsync(journalFD); err != nil {
		t.Fatal(err)
	}
	snapshot, err := loadJournalDirectory(journalFD, uint64(journalStat.Dev))
	if err != nil || snapshot.parsed.lastType != "intent" {
		t.Fatalf("complete record did not recover: %v", err)
	}
}

func TestDirectoryFsyncFailureHasIdempotentExactRecovery(t *testing.T) {
	_, runFD, runStat, journalFD, journalStat := rootJournalDirs(t)
	intent := testIntentRecord()
	if err := writeImmutableRecordAt(journalFD, uint64(journalStat.Dev), "000.intent.record", intent); err != nil {
		t.Fatal(err)
	}
	prefix, err := loadJournalDirectory(journalFD, uint64(journalStat.Dev))
	if err != nil {
		t.Fatal(err)
	}
	makeCatalog := func(j parsedJournal) ([]byte, error) {
		return canonicalBody([]field{
			{"record", "catalog"}, {"observed_at_epoch", "2"}, {"journal_id", j.journalID},
			{"run_id", j.runID}, {"candidate", j.candidate}, {"table_oid", "300"},
			{"index_oid", "400"}, {"constraint_oid", "0"},
			{"catalog_sha256", "4444444444444444444444444444444444444444444444444444444444444444"},
			{"indexdef_sha256", j.indexdefSHA}, {"predicate_sha256", j.predicateSHA},
			{"dependency_sha256", j.dependencySHA}, {"classifier", "INVALID_EXACT"},
			{"status", "catalog_fsynced"},
		}), nil
	}
	body, _ := makeCatalog(prefix.parsed)
	record := appendRecordHash(body, chainedDigest(prefix.parsed.lastHash, body))
	if err := writeImmutableRecordAt(runFD, uint64(runStat.Dev), ".catalog.stage", record); err != nil {
		t.Fatal(err)
	}
	if err := renameAt2NoReplace(runFD, ".catalog.stage", journalFD, "010.catalog.record"); err != nil {
		t.Fatal(err)
	}
	// A directory-fsync error after rename reports failure, but the target is a
	// complete immutable record. Retry uses the previous manifest and exact
	// bytes, then re-fsyncs both record and directory.
	journalFsync = func(int) error { return syscall.EIO }
	if err := journalFsync(journalFD); !errors.Is(err, syscall.EIO) {
		t.Fatalf("directory fsync fault was not injected: %v", err)
	}
	resetJournalHooks()
	snapshot, err := loadJournalDirectory(journalFD, uint64(journalStat.Dev))
	if err != nil {
		t.Fatal(err)
	}
	options := pathOptions{
		journalBasename: "U43-01.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.journal",
		expectedJournal: prefix.manifestSHA,
		allowedDevices:  map[uint64]struct{}{uint64(runStat.Dev): {}},
	}
	if err := recoverPublishedAppend(options, "catalog", runFD, runStat, journalFD, journalStat, snapshot, makeCatalog); err != nil {
		t.Fatal(err)
	}
}
