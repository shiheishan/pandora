//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 main_linux.go 的 pathOptions / parsedJournal / journalSnapshot 与规范化摘要，依赖 fs_linux.go 的可信目录与 openat2 / renameat2 原语，依赖 parse_linux.go 的 parseJournal
// [OUTPUT]: 包内提供 publishIntent、appendCanonical、recoverPublishedAppend、writeImmutableRecordAt、loadJournalDirectory 与段名、清单摘要、记录状态助手
// [POS]: pandora-cic-journal 的日志发布与追加：从 main_linux.go 拆出。每条记录独立成不可变段：先在同设备的 stage 目录里以 O_EXCL 写出、fdatasync 并 fsync，再经 journalRename（renameat2 NOREPLACE，测试可替换）发布并 fsync 父目录；追加持 flock 并校验整份日志 sha256，rename 之后的崩溃窗口经 recoverPublishedAppend 幂等补齐
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"syscall"
)

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
