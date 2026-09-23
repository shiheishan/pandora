//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func preparedBootstrapIntent() v3PreparedIntent {
	return v3PreparedIntent{attemptID: "bootstrap-attempt", releaseContractCoreSHA256: v3H("bootstrap-core"),
		controllerSHA256: v3H("bootstrap-controller"), createdAtEpoch: 1700000000}
}

func preparedBootstrapName(intent v3PreparedIntent) string {
	journalID := SHA256Bytes([]byte(v3BootstrapJournalDomain + intent.attemptID + "\x00" + intent.releaseContractCoreSHA256 + "\x00" + intent.controllerSHA256 + "\x00" + strconv.FormatInt(intent.createdAtEpoch, 10)))
	return intent.attemptID + "." + journalID + ".release-journal-v3"
}

func TestV3BootstrapPreparedCreateExactRetryAndOpen(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, err := os.Open(policy.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	intent := preparedBootstrapIntent()
	created, recovered, err := bootstrapPreparedV3(root, intent)
	if err != nil || recovered || created.State() != V3Prepared || created.Sequence() != 0 {
		t.Fatalf("create: recovered=%v state=%s err=%v", recovered, created.State(), err)
	}
	retry, recovered, err := bootstrapPreparedV3(root, intent)
	if err != nil || !recovered || retry.HeadSHA256() != created.HeadSHA256() || retry.ManifestSHA256() != created.ManifestSHA256() {
		t.Fatalf("retry: recovered=%v err=%v", recovered, err)
	}
	session, err := openV3SessionFromRetainedRoot(root, intent.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.Inspect()
	if err != nil || opened.HeadSHA256() != created.HeadSHA256() {
		t.Fatalf("opened bootstrap mismatch: %v", err)
	}
}

func TestV3BootstrapPreparedResumesEmptyDeterministicDirectory(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, err := os.Open(policy.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	intent := preparedBootstrapIntent()
	name := preparedBootstrapName(intent)
	if err := os.Mkdir(filepath.Join(policy.root, name), 0700); err != nil {
		t.Fatal(err)
	}
	created, recovered, err := bootstrapPreparedV3(root, intent)
	if err != nil || recovered || created.State() != V3Prepared {
		t.Fatalf("empty-dir resume: recovered=%v err=%v", recovered, err)
	}
}

func TestV3BootstrapPreparedRejectsBasenameReplacementAtRootSyncBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		replaceAt  int
		wantReason string
	}{
		{"first-root-sync", 1, "created_binding_changed"},
		{"final-root-sync", 2, "commit_ambiguous"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, _ := linuxJournalFixture(t)
			root, err := os.Open(policy.root)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			intent := preparedBootstrapIntent()
			name := preparedBootstrapName(intent)
			original := filepath.Join(policy.root, name)
			moved := filepath.Join(policy.root, "moved-"+name)
			realFsync := releaseFsync
			count := 0
			t.Cleanup(func() { releaseFsync = realFsync })
			releaseFsync = func(fd int) error {
				if fdMatchesV3Path(fd, policy.root) {
					count++
					if count == tc.replaceAt {
						if err := realFsync(fd); err != nil {
							return err
						}
						if err := os.Rename(original, moved); err != nil {
							return err
						}
						return os.Mkdir(original, 0700)
					}
				}
				return realFsync(fd)
			}
			if _, _, err := bootstrapPreparedV3(root, intent); err == nil || !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("basename replacement not rejected: %v", err)
			}
			replacementEntries, err := os.ReadDir(original)
			if err != nil || len(replacementEntries) != 0 {
				t.Fatalf("replacement directory was written: entries=%d err=%v", len(replacementEntries), err)
			}
			if tc.replaceAt == 2 {
				movedEntries, err := os.ReadDir(moved)
				if err != nil || len(movedEntries) != 1 {
					t.Fatalf("retained journal append missing: entries=%d err=%v", len(movedEntries), err)
				}
			}
		})
	}
}

func TestV3BootstrapPreparedDivergentRetryConflictsWithoutSecondJournal(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	root, err := os.Open(policy.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	intent := preparedBootstrapIntent()
	if _, _, err := bootstrapPreparedV3(root, intent); err != nil {
		t.Fatal(err)
	}
	baseline, err := openV3SessionFromRetainedRoot(root, intent.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := baseline.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	baseline.Close()
	mutations := []struct {
		name string
		edit func(*v3PreparedIntent)
	}{
		{"core", func(v *v3PreparedIntent) { v.releaseContractCoreSHA256 = v3H("other-core") }},
		{"controller", func(v *v3PreparedIntent) { v.controllerSHA256 = v3H("other-controller") }},
		{"created", func(v *v3PreparedIntent) { v.createdAtEpoch++ }},
	}
	for _, mutation := range mutations {
		divergent := intent
		mutation.edit(&divergent)
		if _, _, err := bootstrapPreparedV3(root, divergent); err == nil || !strings.Contains(err.Error(), "identity_conflict") {
			t.Fatalf("%s divergent retry: %v", mutation.name, err)
		}
		entries, err := os.ReadDir(policy.root)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s created a second journal: entries=%d err=%v", mutation.name, len(entries), err)
		}
		retry, recovered, err := bootstrapPreparedV3(root, intent)
		if err != nil || !recovered || retry.HeadSHA256() != original.HeadSHA256() || retry.ManifestSHA256() != original.ManifestSHA256() {
			t.Fatalf("%s damaged original journal: recovered=%v err=%v", mutation.name, recovered, err)
		}
	}
}

func TestV3BootstrapPreparedMkdirAndFirstRootSyncFailuresRecover(t *testing.T) {
	t.Run("mkdir", func(t *testing.T) {
		policy, _ := linuxJournalFixture(t)
		root, err := os.Open(policy.root)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		realMkdir := v3MkdirAt
		t.Cleanup(func() { v3MkdirAt = realMkdir })
		v3MkdirAt = func(int, string, uint32) error { return syscall.EIO }
		if _, _, err := bootstrapPreparedV3(root, preparedBootstrapIntent()); err == nil || !strings.Contains(err.Error(), "bootstrap_mkdir_failed") {
			t.Fatalf("mkdir failure not reported: %v", err)
		}
		entries, _ := os.ReadDir(policy.root)
		if len(entries) != 0 {
			t.Fatal("failed mkdir left a journal")
		}
	})
	t.Run("root-sync", func(t *testing.T) {
		policy, _ := linuxJournalFixture(t)
		root, err := os.Open(policy.root)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		realFsync := releaseFsync
		t.Cleanup(func() { releaseFsync = realFsync })
		releaseFsync = func(fd int) error {
			if fdMatchesV3Path(fd, policy.root) {
				return syscall.EIO
			}
			return realFsync(fd)
		}
		if _, _, err := bootstrapPreparedV3(root, preparedBootstrapIntent()); err == nil || !strings.Contains(err.Error(), "bootstrap_root_sync_failed") {
			t.Fatalf("root sync failure not reported: %v", err)
		}
		releaseFsync = realFsync
		if snapshot, recovered, err := bootstrapPreparedV3(root, preparedBootstrapIntent()); err != nil || recovered || snapshot.State() != V3Prepared {
			t.Fatalf("empty directory did not recover: recovered=%v err=%v", recovered, err)
		}
	})
}

func TestV3BootstrapPreparedPublishFailureMatrixRecovers(t *testing.T) {
	cases := []struct {
		name    string
		install func(t *testing.T, policy pathPolicy)
	}{
		{"link-before", func(t *testing.T, _ pathPolicy) {
			real := v3LinkAnonymous
			t.Cleanup(func() { v3LinkAnonymous = real })
			v3LinkAnonymous = func(int, int, string) error { return syscall.EIO }
		}},
		{"link-after", func(t *testing.T, _ pathPolicy) {
			real := v3LinkAnonymous
			t.Cleanup(func() { v3LinkAnonymous = real })
			v3LinkAnonymous = func(fd, dirFD int, name string) error {
				if err := real(fd, dirFD, name); err != nil {
					return err
				}
				return syscall.EIO
			}
		}},
		{"journal-sync", func(t *testing.T, policy pathPolicy) {
			real := releaseFsync
			t.Cleanup(func() { releaseFsync = real })
			releaseFsync = func(fd int) error {
				var stat syscall.Stat_t
				if syscall.Fstat(fd, &stat) == nil && stat.Mode&syscall.S_IFMT == syscall.S_IFDIR && !fdMatchesV3Path(fd, policy.root) {
					return syscall.EIO
				}
				return real(fd)
			}
		}},
		{"final-root-sync", func(t *testing.T, policy pathPolicy) {
			real := releaseFsync
			count := 0
			t.Cleanup(func() { releaseFsync = real })
			releaseFsync = func(fd int) error {
				if fdMatchesV3Path(fd, policy.root) {
					count++
					if count == 2 {
						return syscall.EIO
					}
				}
				return real(fd)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, _ := linuxJournalFixture(t)
			root, err := os.Open(policy.root)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			tc.install(t, policy)
			if _, _, err := bootstrapPreparedV3(root, preparedBootstrapIntent()); err == nil {
				t.Fatal("injected publish failure not reported")
			}
			v3LinkAnonymous = linkV3Anonymous
			releaseFsync = syscall.Fsync
			snapshot, recovered, err := bootstrapPreparedV3(root, preparedBootstrapIntent())
			if err != nil || snapshot.State() != V3Prepared {
				t.Fatalf("retry failed: recovered=%v err=%v", recovered, err)
			}
			entries, _ := os.ReadDir(policy.root)
			if len(entries) != 1 {
				t.Fatalf("journal count=%d", len(entries))
			}
		})
	}
}

func fdMatchesV3Path(fd int, path string) bool {
	var fdStat syscall.Stat_t
	var pathStat syscall.Stat_t
	return syscall.Fstat(fd, &fdStat) == nil && syscall.Stat(path, &pathStat) == nil && fdStat.Dev == pathStat.Dev && fdStat.Ino == pathStat.Ino
}
