//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeV3JournalFixture(t *testing.T, policy pathPolicy, terminal V3State) (*os.File, V3Snapshot, string) {
	t.Helper()
	records, snapshot := v3RecordsTo(t, terminal)
	name := snapshot.AttemptID() + "." + snapshot.JournalID() + ".release-journal-v3"
	directory := filepath.Join(policy.root, name)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for segment, data := range records {
		path := filepath.Join(directory, segment)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0400); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.Open(policy.root)
	if err != nil {
		t.Fatal(err)
	}
	return root, snapshot, name
}

func TestV3SessionLockIsIndependentFromCallerDescriptor(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, snapshot, _ := writeV3JournalFixture(t, policy, V3LayoutSwitched)
	session, err := openV3SessionFromRetainedRoot(root, snapshot.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	// Unlocking the caller-owned description must not release Session's SH lock.
	if err := syscall.Flock(int(root.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	competitor, err := reopenV3DirectoryFD(int(root.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(competitor)
	if err := syscall.Flock(competitor, syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(competitor, syscall.LOCK_UN)
		t.Fatal("caller unlock released retained v3 session root lock")
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.InspectLayoutSwitched(); err != nil {
		t.Fatalf("caller close invalidated retained session: %v", err)
	}
}

func TestV3SessionRootContentionIsNonblockingAndClassified(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, snapshot, _ := writeV3JournalFixture(t, policy, V3LayoutSwitched)
	defer root.Close()
	holder, err := reopenV3DirectoryFD(int(root.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(holder)
	if err := syscall.Flock(holder, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(holder, syscall.LOCK_UN)
	started := time.Now()
	session, err := openV3SessionFromRetainedRoot(root, snapshot.AttemptID())
	if session != nil {
		_ = session.Close()
		t.Fatal("v3 session opened while root was exclusively locked")
	}
	if !errors.Is(err, ErrV3SessionBusy) || time.Since(started) > time.Second {
		t.Fatalf("root contention was not bounded/classified: elapsed=%s err=%v", time.Since(started), err)
	}
	if err := syscall.Flock(holder, syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	session, err = openV3SessionFromRetainedRoot(root, snapshot.AttemptID())
	if err != nil {
		t.Fatalf("v3 session did not recover after root unlock: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestV3SessionRejectsSecondJournalLockAndSyncFailureIsNonMutating(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, snapshot, _ := writeV3JournalFixture(t, policy, V3LayoutSwitched)
	defer root.Close()
	first, err := openV3SessionFromRetainedRoot(root, snapshot.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := openV3SessionFromRetainedRoot(root, snapshot.AttemptID()); err == nil {
		_ = second.Close()
		t.Fatal("second exclusive v3 journal session opened")
	}
	realFsync := releaseFsync
	t.Cleanup(func() { releaseFsync = realFsync })
	releaseFsync = func(int) error { return syscall.EIO }
	if _, err := first.Sync(); err == nil {
		t.Fatal("injected v3 sync failure not reported")
	}
	releaseFsync = realFsync
	inspected, err := first.InspectLayoutSwitched()
	if err != nil {
		t.Fatal(err)
	}
	if inspected.HeadSHA256() != snapshot.HeadSHA256() || inspected.ManifestSHA256() != snapshot.ManifestSHA256() {
		t.Fatal("failed sync mutated v3 journal")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := openV3SessionFromRetainedRoot(root, snapshot.AttemptID())
	if err != nil {
		t.Fatalf("journal lock was not released by Close: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestV3SessionRetainsReplaysAndSyncsExactBoundary(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, expected, _ := writeV3JournalFixture(t, policy, V3LayoutSwitched)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, expected.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	actual, err := session.InspectLayoutSwitched()
	if err != nil {
		t.Fatal(err)
	}
	if actual.HeadSHA256() != expected.HeadSHA256() || actual.ManifestSHA256() != expected.ManifestSHA256() || actual.Sequence() != 6 {
		t.Fatal("retained replay identity mismatch")
	}
	synced, err := session.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if synced.HeadSHA256() != actual.HeadSHA256() || synced.ManifestSHA256() != actual.ManifestSHA256() {
		t.Fatal("sync changed journal")
	}
	if synced.ReleaseContractCoreSHA256() != expected.ReleaseContractCoreSHA256() || synced.ControllerSHA256() != expected.ControllerSHA256() {
		t.Fatal("prepared contract projection changed")
	}
}

func TestV3SessionRejectsWritableUnknownSymlinkAndHardlinkRecords(t *testing.T) {
	mutations := []struct {
		name  string
		apply func(t *testing.T, directory string)
	}{
		{"writable", func(t *testing.T, directory string) {
			if err := os.Chmod(filepath.Join(directory, v3Segments[V3Prepared]), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown", func(t *testing.T, directory string) {
			if err := os.WriteFile(filepath.Join(directory, "777.unknown.record"), []byte("x"), 0400); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, directory string) {
			path := filepath.Join(directory, v3Segments[V3Prepared])
			target := path + ".target"
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, directory string) {
			path := filepath.Join(directory, v3Segments[V3Prepared])
			if err := os.Link(path, path+".hardlink"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			policy, _ := linuxJournalFixture(t)
			root, snapshot, name := writeV3JournalFixture(t, policy, V3LayoutSwitched)
			defer root.Close()
			mutation.apply(t, filepath.Join(policy.root, name))
			if session, err := openV3SessionFromRetainedRoot(root, snapshot.AttemptID()); err == nil {
				_ = session.Close()
				t.Fatal("unsafe v3 record inventory accepted")
			}
		})
	}
}

func TestV3SessionRejectsBasenameReplacementAndLegacyJournal(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, boundary, name := writeV3JournalFixture(t, policy, V3LayoutSwitched)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, boundary.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := os.Rename(filepath.Join(policy.root, name), filepath.Join(policy.root, "retained-old."+boundary.JournalID()+".release-journal-v3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(policy.root, name), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Inspect(); err == nil || !strings.Contains(err.Error(), "journal_v3_retained_binding_changed") {
		t.Fatalf("basename replacement result=%v", err)
	}

	legacyPolicy, _ := linuxJournalFixture(t)
	legacyRoot, legacy := writeV2JournalFixture(t, legacyPolicy, stateLayoutSwitched)
	defer legacyRoot.Close()
	if opened, err := openV3SessionFromRetainedRoot(legacyRoot, legacy.Prepared.ReleaseAttemptID); err == nil {
		_ = opened.Close()
		t.Fatal("legacy journal opened as v3")
	}
}

func TestV3SessionCloseAndNilFailClosed(t *testing.T) {
	var nilSession *V3Session
	if _, err := nilSession.Inspect(); err == nil {
		t.Fatal("nil inspect accepted")
	}
	if _, err := nilSession.Sync(); err == nil {
		t.Fatal("nil sync accepted")
	}
	if err := nilSession.Close(); err != nil {
		t.Fatal(err)
	}
	policy, _ := linuxJournalFixture(t)
	root, snapshot, _ := writeV3JournalFixture(t, policy, V3Prepared)
	defer root.Close()
	session, err := openV3SessionFromRetainedRoot(root, snapshot.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Inspect(); err == nil {
		t.Fatal("closed session inspect accepted")
	}
	if _, err := session.Sync(); err == nil {
		t.Fatal("closed session sync accepted")
	}
	if _, err := os.Stat(policy.root); errors.Is(err, os.ErrNotExist) {
		t.Fatal("session close removed journal root")
	}
}
