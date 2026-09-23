//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"os"
	"strconv"
	"syscall"
)

const v3BootstrapJournalDomain = "PANDORA\x00CA42-RELEASE-JOURNAL-BOOTSTRAP\x00V3\x00"

var v3MkdirAt = mkdirAt

type v3PreparedIntent struct {
	attemptID                 string
	releaseContractCoreSHA256 string
	controllerSHA256          string
	createdAtEpoch            int64
}

// bootstrapPreparedV3 is package-private until a production opener supplies
// the canonical parent/root-basename capability. It creates or exact-recovers
// one deterministic PREPARED journal without named temporary record files.
func bootstrapPreparedV3(retainedRoot *os.File, intent v3PreparedIntent) (V3Snapshot, bool, error) {
	if os.Geteuid() != 0 || retainedRoot == nil || !v3TokenRE.MatchString(intent.attemptID) {
		return V3Snapshot{}, false, deny("journal_v3_bootstrap_identity_invalid")
	}
	journalID := SHA256Bytes([]byte(v3BootstrapJournalDomain + intent.attemptID + "\x00" + intent.releaseContractCoreSHA256 + "\x00" + intent.controllerSHA256 + "\x00" + strconv.FormatInt(intent.createdAtEpoch, 10)))
	prepared, _, err := v3PreparedRecordBytes(v3PreparedFields{JournalID: journalID, AttemptID: intent.attemptID,
		ReleaseContractCoreSHA256: intent.releaseContractCoreSHA256, ControllerSHA256: intent.controllerSHA256,
		CreatedAtEpoch: intent.createdAtEpoch})
	if err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_candidate_invalid", err)
	}
	candidate, err := ParseV3(map[string][]byte{v3Segments[V3Prepared]: prepared})
	if err != nil {
		return V3Snapshot{}, false, err
	}
	originalFD := int(retainedRoot.Fd())
	if originalFD < 3 {
		return V3Snapshot{}, false, deny("journal_root_fd_must_be_retained")
	}
	var original syscall.Stat_t
	if err := syscall.Fstat(originalFD, &original); err != nil {
		return V3Snapshot{}, false, err
	}
	rootFD, err := reopenV3DirectoryFD(originalFD)
	if err != nil {
		return V3Snapshot{}, false, err
	}
	defer syscall.Close(rootFD)
	allowed := map[uint64]struct{}{uint64(original.Dev): {}}
	rootStat, err := validateDirectoryFD(rootFD, allowed)
	if err != nil {
		return V3Snapshot{}, false, err
	}
	if !sameFileIdentity(original, rootStat) || rootStat.Mode&07777 != 0700 {
		return V3Snapshot{}, false, deny("journal_v3_bootstrap_root_invalid")
	}
	if err := syscall.Flock(rootFD, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return V3Snapshot{}, false, err
	}
	defer syscall.Flock(rootFD, syscall.LOCK_UN)
	expectedName := intent.attemptID + "." + journalID + ".release-journal-v3"
	name, exists, err := findV3BootstrapJournal(rootFD, intent.attemptID, expectedName, uint64(rootStat.Dev), allowed)
	if err != nil {
		return V3Snapshot{}, false, err
	}
	journalFD := -1
	var journalStat syscall.Stat_t
	if !exists {
		if err := v3MkdirAt(rootFD, expectedName, 0700); err != nil {
			return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_mkdir_failed", err)
		}
		journalFD, journalStat, err = openJournalDirectory(rootFD, expectedName, uint64(rootStat.Dev), allowed)
		if err != nil {
			return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_created_open_failed", err)
		}
		defer syscall.Close(journalFD)
		if err := releaseFsync(rootFD); err != nil {
			return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_root_sync_failed", err)
		}
		name = expectedName
		if err := validateV3BootstrapNameBinding(rootFD, name, journalStat, uint64(rootStat.Dev), allowed); err != nil {
			return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_created_binding_changed", err)
		}
	}
	if journalFD < 0 {
		journalFD, journalStat, err = openJournalDirectory(rootFD, name, uint64(rootStat.Dev), allowed)
		if err != nil {
			return V3Snapshot{}, false, err
		}
		defer syscall.Close(journalFD)
	}
	if err := syscall.Flock(journalFD, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return V3Snapshot{}, false, err
	}
	defer syscall.Flock(journalFD, syscall.LOCK_UN)
	names, err := listDirectoryNames(journalFD)
	if err != nil {
		return V3Snapshot{}, false, err
	}
	session := &V3Session{lease: &v3SessionLease{}, rootFD: rootFD, journalFD: journalFD, rootStat: rootStat, journalStat: journalStat,
		journalName: name, policy: pathPolicy{expectedDevice: uint64(rootStat.Dev), allowedDevices: allowed}}
	if len(names) != 0 {
		current, err := session.inspectLocked()
		if err != nil {
			return V3Snapshot{}, false, err
		}
		if current.Sequence() != 0 || current.HeadSHA256() != candidate.HeadSHA256() || current.ManifestSHA256() != candidate.ManifestSHA256() ||
			!bytesEqualV3(current.records[v3Segments[V3Prepared]], prepared) {
			return V3Snapshot{}, false, deny("journal_v3_bootstrap_exact_retry_conflict")
		}
		if err := syncV3Snapshot(journalFD, uint64(journalStat.Dev), current); err != nil {
			return V3Snapshot{}, false, err
		}
		if err := releaseFsync(rootFD); err != nil {
			return V3Snapshot{}, false, err
		}
		if err := session.validateRetainedBinding(); err != nil {
			return V3Snapshot{}, false, err
		}
		reloaded, err := session.inspectLocked()
		if err != nil || reloaded.HeadSHA256() != candidate.HeadSHA256() || reloaded.ManifestSHA256() != candidate.ManifestSHA256() {
			return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_exact_reload_mismatch", err)
		}
		return reloaded, true, nil
	}
	_, recovered, err := session.publishV3Locked(v3Segments[V3Prepared], prepared, candidate)
	if err != nil {
		return V3Snapshot{}, false, err
	}
	if err := releaseFsync(rootFD); err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_commit_ambiguous", err)
	}
	if err := session.validateRetainedBinding(); err != nil {
		return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_commit_ambiguous", err)
	}
	reloaded, err := session.inspectLocked()
	if err != nil || reloaded.HeadSHA256() != candidate.HeadSHA256() || reloaded.ManifestSHA256() != candidate.ManifestSHA256() {
		return V3Snapshot{}, false, denyErr("journal_v3_bootstrap_commit_ambiguous", err)
	}
	return reloaded, recovered, nil
}

func validateV3BootstrapNameBinding(rootFD int, name string, expected syscall.Stat_t, device uint64, allowed map[uint64]struct{}) error {
	fd, actual, err := openJournalDirectory(rootFD, name, device, allowed)
	if err != nil {
		return err
	}
	_ = syscall.Close(fd)
	if !sameFileIdentity(actual, expected) {
		return deny("journal_v3_bootstrap_name_binding_changed")
	}
	return nil
}

func findV3BootstrapJournal(rootFD int, attemptID, expectedName string, device uint64, allowed map[uint64]struct{}) (string, bool, error) {
	names, err := listDirectoryNames(rootFD)
	if err != nil {
		return "", false, err
	}
	match := ""
	for _, name := range names {
		parts := journalV3RE.FindStringSubmatch(name)
		if parts == nil {
			return "", false, deny("unknown_entry_in_journal_v3_root")
		}
		fd, _, err := openJournalDirectory(rootFD, name, device, allowed)
		if err != nil {
			return "", false, err
		}
		entries, listErr := listDirectoryNames(fd)
		if parts[1] != attemptID || name != expectedName {
			if listErr != nil || len(entries) == 0 {
				_ = syscall.Close(fd)
				return "", false, deny("journal_v3_other_bootstrap_invalid")
			}
			snapshot, loadErr := loadV3Snapshot(fd, device)
			_ = syscall.Close(fd)
			if loadErr != nil || validateV3JournalName(name, snapshot) != nil {
				return "", false, denyErr("journal_v3_other_journal_invalid", loadErr)
			}
			if parts[1] == attemptID {
				return "", false, deny("journal_v3_bootstrap_identity_conflict")
			}
			continue
		}
		_ = syscall.Close(fd)
		if match != "" || listErr != nil {
			return "", false, deny("multiple_release_journal_v3_for_attempt")
		}
		match = name
	}
	return match, match != "", nil
}

func bytesEqualV3(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
