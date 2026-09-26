//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 store_linux.go 的 pathPolicy、错误模型、故障注入点与 stage / temp 名称文法，依赖 store_fs_linux.go 的可信文件系统原语，依赖 model.go 的 releaseJournalSnapshot 与状态机
// [OUTPUT]: 包内提供 prepareJournal、advanceJournal、recoverAdvance、discoverJournal、loadSnapshot、syncSnapshot 等日志读写流程
// [POS]: platform/releasejournal 发布日志 v1 命令行（pandora-release-journal）的准备与推进：从 store_linux.go 拆出。准备幂等（同一请求可安全重跑，samePreparedRequest），推进持 flock 做期望哈希 CAS，rename 之后的崩溃窗口经 recoverAdvance 按原请求精确补齐，分歧即拒绝
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package releasejournal

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"syscall"
)

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
