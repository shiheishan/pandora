//go:build linux && (amd64 || arm64)

package ca42nonce

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	nonceProcessHelperEnv = "PANDORA_CA42_NONCE_PROCESS_HELPER"
	nonceProcessRootEnv   = "PANDORA_CA42_NONCE_PROCESS_ROOT"
	nonceProcessLabelEnv  = "PANDORA_CA42_NONCE_PROCESS_LABEL"
	nonceProcessCrashEnv  = "PANDORA_CA42_NONCE_PROCESS_CRASH"
	nonceProcessStartEnv  = "PANDORA_CA42_NONCE_PROCESS_START"
	nonceProcessMarkerEnv = "PANDORA_CA42_NONCE_PROCESS_MARKER_FD"
)

// TestNonceStoreProcessHelper is entered only through a fresh exec of the test
// binary. It intentionally uses os.Exit at durable boundaries so kernel lock
// release and restart behavior are exercised rather than mocked.
func TestNonceStoreProcessHelper(t *testing.T) {
	action := os.Getenv(nonceProcessHelperEnv)
	if action == "" {
		return
	}
	rootPath := os.Getenv(nonceProcessRootEnv)
	label := os.Getenv(nonceProcessLabelEnv)
	if rootPath == "" || (label != "a" && label != "b") || (action != "reserve" && action != "commit" && action != "recover" && action != "publish") {
		fmt.Println("NONCE_RESULT=invalid_helper_input")
		os.Exit(2)
	}
	if start := os.Getenv(nonceProcessStartEnv); start != "" {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(start); err == nil {
				break
			}
			if time.Now().After(deadline) {
				fmt.Println("NONCE_RESULT=start_timeout")
				os.Exit(3)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	crash := os.Getenv(nonceProcessCrashEnv)
	crashNow := func(stage string) {
		if crash == stage {
			if os.Getenv(nonceProcessMarkerEnv) == "3" {
				marker := os.NewFile(3, "nonce-crash-marker")
				if marker != nil {
					_, _ = marker.Write([]byte(stage))
				}
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			os.Exit(72)
		}
	}
	hooks := nonceStoreHooks{
		afterDirectoryCreate: func(string) error { crashNow("post_mkdir"); return nil },
		afterRecordLink: func(name string) error {
			if (action == "reserve" && name == ReservedRecordName) || (action == "commit" && name == CommittedRecordName) ||
				(action == "recover" && name == RecoveryRecordName) {
				crashNow("post_link")
			}
			return nil
		},
		afterDirectorySync: func(operation string) error {
			if (action == "reserve" && operation == ReservedRecordName) || (action == "commit" && operation == CommittedRecordName) ||
				(action == "recover" && operation == RecoveryRecordName) {
				crashNow("post_record_dir_fsync")
			}
			if operation == "root" {
				crashNow("post_root_fsync")
			}
			return nil
		},
	}
	if action == "publish" {
		directory, err := os.Open(rootPath)
		if err != nil {
			fmt.Println("NONCE_RESULT=root_open_error")
			os.Exit(4)
		}
		defer directory.Close()
		var stat syscall.Stat_t
		if err := syscall.Fstat(int(directory.Fd()), &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFDIR ||
			stat.Uid != 0 || stat.Gid != 0 || stat.Mode&07777 != 0700 {
			fmt.Println("NONCE_RESULT=publish_directory_invalid")
			os.Exit(8)
		}
		data, _, err := reservationRecordBytes(nonceStoreFields(label))
		if err != nil {
			fmt.Println("NONCE_RESULT=publish_candidate_invalid")
			os.Exit(9)
		}
		recovered, err := publishNonceRecord(int(directory.Fd()), ReservedRecordName, data, uint64(stat.Dev), hooks)
		if err != nil {
			if strings.Contains(err.Error(), "divergent retry") || strings.Contains(err.Error(), "concurrent divergent publication") {
				fmt.Println("NONCE_RESULT=conflict")
				return
			}
			fmt.Printf("NONCE_RESULT=operation_error:%T\n", err)
			os.Exit(10)
		}
		if recovered {
			fmt.Println("NONCE_RESULT=recovered")
		} else {
			fmt.Println("NONCE_RESULT=created")
		}
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		root, err := os.Open(rootPath)
		if err != nil {
			fmt.Println("NONCE_RESULT=root_open_error")
			os.Exit(4)
		}
		store, err := openRetainedStore(root, hooks)
		if err != nil {
			_ = root.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
				if time.Now().Before(deadline) {
					time.Sleep(2 * time.Millisecond)
					continue
				}
				fmt.Println("NONCE_RESULT=lock_timeout")
				os.Exit(5)
			}
			fmt.Println("NONCE_RESULT=store_open_error")
			os.Exit(6)
		}
		fields := nonceStoreFields(label)
		var session *nonceSession
		var recovered bool
		var operationErr error
		if action == "reserve" {
			session, recovered, operationErr = store.reserve(fields)
		} else {
			session, operationErr = store.openExisting(fields.NonceID, fields.TransactionID)
			if operationErr == nil && action == "commit" {
				_, recovered, operationErr = session.commit(nonceCommittedFields(fields))
			}
			if operationErr == nil && action == "recover" {
				_, recovered, operationErr = session.recover(nonceRecoveryFields())
			}
		}
		if session != nil {
			_ = session.close()
		}
		_ = store.close()
		_ = root.Close()
		if operationErr != nil {
			if errors.Is(operationErr, ErrReservationIncomplete) {
				fmt.Println("NONCE_RESULT=incomplete")
				return
			}
			if strings.Contains(operationErr.Error(), "replay transaction conflict") || strings.Contains(operationErr.Error(), "divergent retry") ||
				strings.Contains(operationErr.Error(), "terminal fork") {
				fmt.Println("NONCE_RESULT=conflict")
				return
			}
			fmt.Printf("NONCE_RESULT=operation_error:%T\n", operationErr)
			os.Exit(7)
		}
		if recovered {
			fmt.Println("NONCE_RESULT=recovered")
		} else {
			fmt.Println("NONCE_RESULT=created")
		}
		return
	}
}

func TestRetainedStoreExecCrashRecoveryMatrix(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	for _, stage := range []string{"post_mkdir", "post_link", "post_record_dir_fsync", "post_root_fsync"} {
		t.Run(stage, func(t *testing.T) {
			rootPath := newProcessNonceRoot(t)
			result, exitCode, signal, marker := runNonceProcess(t, "reserve", rootPath, "a", stage, "")
			if signal != syscall.SIGKILL || marker != stage {
				t.Fatalf("crash stage=%s exit=%d signal=%v marker=%q output=%s", stage, exitCode, signal, marker, result)
			}
			before := captureProcessNonceDisk(t, rootPath, "a")
			if stage == "post_mkdir" && before.hasRecord {
				t.Fatalf("post-mkdir unexpectedly published a record: %+v", before)
			}
			if stage != "post_mkdir" && !before.hasRecord {
				t.Fatalf("stage %s did not publish the expected record", stage)
			}
			result, exitCode, signal, marker = runNonceProcess(t, "reserve", rootPath, "a", "", "")
			wantResult := "recovered"
			if stage == "post_mkdir" {
				wantResult = "incomplete"
			}
			if exitCode != 0 || !strings.Contains(result, "NONCE_RESULT="+wantResult) {
				t.Fatalf("recovery stage=%s exit=%d signal=%v marker=%q output=%s", stage, exitCode, signal, marker, result)
			}
			after := captureProcessNonceDisk(t, rootPath, "a")
			if before.dirDev != after.dirDev || before.dirIno != after.dirIno {
				t.Fatalf("stage %s replaced nonce directory before=%+v after=%+v", stage, before, after)
			}
			if before.hasRecord && (!after.hasRecord || before.recordDev != after.recordDev || before.recordIno != after.recordIno ||
				before.recordSize != after.recordSize || before.recordMtime != after.recordMtime || before.recordCtime != after.recordCtime ||
				!bytes.Equal(before.recordBytes, after.recordBytes)) {
				t.Fatalf("stage %s exact recovery changed visible record before=%+v after=%+v", stage, before, after)
			}
			if stage == "post_mkdir" {
				if after.hasRecord {
					t.Fatal("ordinary retry filled an incomplete fenced nonce directory")
				}
			} else {
				assertProcessNonceWinner(t, rootPath, "a")
			}
			result, exitCode, signal, marker = runNonceProcess(t, "reserve", rootPath, "b", "", "")
			if exitCode != 0 || !strings.Contains(result, "NONCE_RESULT=conflict") {
				t.Fatalf("divergent stage=%s exit=%d signal=%v marker=%q output=%s", stage, exitCode, signal, marker, result)
			}
			if stage != "post_mkdir" {
				assertProcessNonceWinner(t, rootPath, "a")
			}
		})
	}
}

func TestRetainedStoreExecTerminalCrashRecoveryMatrix(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	for _, action := range []string{"commit", "recover"} {
		for _, stage := range []string{"post_link", "post_record_dir_fsync"} {
			t.Run(action+"_"+stage, func(t *testing.T) {
				rootPath := newProcessNonceRoot(t)
				if result, exitCode, signal, marker := runNonceProcess(t, "reserve", rootPath, "a", "", ""); exitCode != 0 || signal != 0 || marker != "" || !strings.Contains(result, "NONCE_RESULT=created") {
					t.Fatalf("terminal setup exit=%d signal=%v marker=%q output=%s", exitCode, signal, marker, result)
				}
				result, exitCode, signal, marker := runNonceProcess(t, action, rootPath, "a", stage, "")
				if signal != syscall.SIGKILL || marker != stage {
					t.Fatalf("terminal crash action=%s stage=%s exit=%d signal=%v marker=%q output=%s", action, stage, exitCode, signal, marker, result)
				}
				before := captureProcessNonceTerminal(t, rootPath, action)
				result, exitCode, signal, marker = runNonceProcess(t, action, rootPath, "a", "", "")
				if exitCode != 0 || signal != 0 || marker != "" || !strings.Contains(result, "NONCE_RESULT=recovered") {
					t.Fatalf("terminal recovery action=%s stage=%s exit=%d signal=%v marker=%q output=%s", action, stage, exitCode, signal, marker, result)
				}
				after := captureProcessNonceTerminal(t, rootPath, action)
				if before.recordDev != after.recordDev || before.recordIno != after.recordIno || before.recordSize != after.recordSize ||
					before.recordMtime != after.recordMtime || before.recordCtime != after.recordCtime || !bytes.Equal(before.recordBytes, after.recordBytes) {
					t.Fatalf("terminal exact recovery changed record before=%+v after=%+v", before, after)
				}
				other := "recover"
				if action == "recover" {
					other = "commit"
				}
				if result, exitCode, _, _ := runNonceProcess(t, other, rootPath, "a", "", ""); exitCode != 0 || !strings.Contains(result, "NONCE_RESULT=conflict") {
					t.Fatalf("opposite terminal accepted action=%s output=%s", other, result)
				}
			})
		}
	}
}

type nonceDiskEvidence struct {
	dirDev, dirIno           uint64
	hasRecord                bool
	recordDev, recordIno     uint64
	recordSize               int64
	recordMtime, recordCtime syscall.Timespec
	recordBytes              []byte
}

func captureProcessNonceDisk(t *testing.T, rootPath, label string) nonceDiskEvidence {
	t.Helper()
	fields := nonceStoreFields(label)
	directoryPath := filepath.Join(rootPath, nonceDirectoryName(fields.NonceID, fields.TransactionID))
	var directory syscall.Stat_t
	if err := syscall.Stat(directoryPath, &directory); err != nil {
		t.Fatal(err)
	}
	evidence := nonceDiskEvidence{dirDev: uint64(directory.Dev), dirIno: directory.Ino}
	entries, err := os.ReadDir(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		return evidence
	}
	if len(entries) != 1 || entries[0].Name() != ReservedRecordName {
		t.Fatalf("unexpected crash inventory: %v", entries)
	}
	recordPath := filepath.Join(directoryPath, ReservedRecordName)
	var record syscall.Stat_t
	if err := syscall.Stat(recordPath, &record); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	evidence.hasRecord = true
	evidence.recordDev, evidence.recordIno, evidence.recordSize = uint64(record.Dev), record.Ino, record.Size
	evidence.recordMtime, evidence.recordCtime, evidence.recordBytes = record.Mtim, record.Ctim, data
	return evidence
}

func TestRetainedStoreConcurrentExactCreateCAS(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	rootPath := newProcessNonceRoot(t)
	results := runConcurrentNonceProcesses(t, rootPath, 16, func(int) string { return "a" })
	counts := countNonceResults(results)
	if counts["created"] != 1 || counts["recovered"] != 15 || len(counts) != 2 {
		t.Fatalf("exact CAS results=%v raw=%v", counts, results)
	}
	assertProcessNonceWinner(t, rootPath, "a")
}

func TestRetainedStoreConcurrentDivergentCAS(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	rootPath := newProcessNonceRoot(t)
	results := runConcurrentNonceProcesses(t, rootPath, 16, func(index int) string {
		if index%2 == 0 {
			return "a"
		}
		return "b"
	})
	counts := countNonceResults(results)
	if counts["created"] != 1 || counts["recovered"] != 7 || counts["conflict"] != 8 || len(counts) != 3 {
		t.Fatalf("divergent CAS results=%v raw=%v", counts, results)
	}
	winner := "a"
	for index := 1; index < len(results); index += 2 {
		if strings.Contains(results[index], "NONCE_RESULT=created") || strings.Contains(results[index], "NONCE_RESULT=recovered") {
			winner = "b"
			break
		}
	}
	assertProcessNonceWinner(t, rootPath, winner)
}

// These tests deliberately bypass the retained-store root flock and race only
// the package-private O_TMPFILE + linkat publication primitive. Store-level
// process tests above prove lock serialization; they cannot exercise the
// concurrent EEXIST reconciliation branch.
func TestPublishNonceRecordConcurrentExactCAS(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	directoryPath := newProcessNonceRoot(t)
	results := runConcurrentNonceActionProcesses(t, "publish", directoryPath, 16, func(int) string { return "a" })
	counts := countNonceResults(results)
	if counts["created"] != 1 || counts["recovered"] != 15 || len(counts) != 2 {
		t.Fatalf("exact primitive CAS results=%v raw=%v", counts, results)
	}
	assertPublishedPrimitiveWinner(t, directoryPath, "a")
}

func TestPublishNonceRecordConcurrentDivergentCAS(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned fixture")
	}
	directoryPath := newProcessNonceRoot(t)
	results := runConcurrentNonceActionProcesses(t, "publish", directoryPath, 16, func(index int) string {
		if index%2 == 0 {
			return "a"
		}
		return "b"
	})
	counts := countNonceResults(results)
	if counts["created"] != 1 || counts["recovered"] != 7 || counts["conflict"] != 8 || len(counts) != 3 {
		t.Fatalf("divergent primitive CAS results=%v raw=%v", counts, results)
	}
	winner := "a"
	for index := 1; index < len(results); index += 2 {
		if strings.Contains(results[index], "NONCE_RESULT=created") || strings.Contains(results[index], "NONCE_RESULT=recovered") {
			winner = "b"
			break
		}
	}
	assertPublishedPrimitiveWinner(t, directoryPath, winner)
}

func newProcessNonceRoot(t *testing.T) string {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "nonce-root")
	if err := os.Mkdir(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	return rootPath
}

func runNonceProcess(t *testing.T, action, rootPath, label, crash, start string) (string, int, syscall.Signal, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNonceStoreProcessHelper$", "-test.count=1")
	markerRead, markerWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer markerRead.Close()
	cmd.ExtraFiles = []*os.File{markerWrite}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.Env = append(os.Environ(), nonceProcessHelperEnv+"="+action, nonceProcessRootEnv+"="+rootPath,
		nonceProcessLabelEnv+"="+label, nonceProcessCrashEnv+"="+crash, nonceProcessStartEnv+"="+start, nonceProcessMarkerEnv+"=3")
	err = cmd.Start()
	_ = markerWrite.Close()
	if err == nil {
		err = cmd.Wait()
	}
	markerBytes, _ := io.ReadAll(io.LimitReader(markerRead, 128))
	if ctx.Err() != nil {
		t.Fatalf("nonce child timed out: %v output=%s", ctx.Err(), output.String())
	}
	if err == nil {
		return output.String(), 0, 0, string(markerBytes)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("nonce child start/wait failed: %v output=%s", err, output.String())
	}
	waitStatus, ok := exitErr.Sys().(syscall.WaitStatus)
	if ok && waitStatus.Signaled() {
		return output.String(), exitErr.ExitCode(), waitStatus.Signal(), string(markerBytes)
	}
	return output.String(), exitErr.ExitCode(), 0, string(markerBytes)
}

func captureProcessNonceTerminal(t *testing.T, rootPath, action string) nonceDiskEvidence {
	t.Helper()
	fields := nonceStoreFields("a")
	name := CommittedRecordName
	if action == "recover" {
		name = RecoveryRecordName
	}
	recordPath := filepath.Join(rootPath, nonceDirectoryName(fields.NonceID, fields.TransactionID), name)
	var record syscall.Stat_t
	if err := syscall.Stat(recordPath, &record); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	return nonceDiskEvidence{hasRecord: true, recordDev: uint64(record.Dev), recordIno: record.Ino, recordSize: record.Size,
		recordMtime: record.Mtim, recordCtime: record.Ctim, recordBytes: data}
}

func runConcurrentNonceProcesses(t *testing.T, rootPath string, count int, label func(int) string) []string {
	return runConcurrentNonceActionProcesses(t, "reserve", rootPath, count, label)
}

func runConcurrentNonceActionProcesses(t *testing.T, action, rootPath string, count int, label func(int) string) []string {
	t.Helper()
	startPath := filepath.Join(t.TempDir(), "start")
	type child struct {
		cmd    *exec.Cmd
		buffer bytes.Buffer
		cancel context.CancelFunc
	}
	children := make([]child, count)
	for index := range children {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		children[index].cancel = cancel
		children[index].cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNonceStoreProcessHelper$", "-test.count=1")
		children[index].cmd.Env = append(os.Environ(), nonceProcessHelperEnv+"="+action, nonceProcessRootEnv+"="+rootPath,
			nonceProcessLabelEnv+"="+label(index), nonceProcessCrashEnv+"=", nonceProcessStartEnv+"="+startPath)
		children[index].cmd.Stdout, children[index].cmd.Stderr = &children[index].buffer, &children[index].buffer
		if err := children[index].cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(startPath, []byte("start\n"), 0600); err != nil {
		t.Fatal(err)
	}
	results := make([]string, count)
	var wait sync.WaitGroup
	wait.Add(count)
	for index := range children {
		go func(index int) {
			defer wait.Done()
			err := children[index].cmd.Wait()
			children[index].cancel()
			results[index] = children[index].buffer.String()
			if err != nil {
				results[index] += fmt.Sprintf("\nWAIT_ERROR=%v", err)
			}
		}(index)
	}
	wait.Wait()
	for index, result := range results {
		if strings.Contains(result, "WAIT_ERROR=") || !strings.Contains(result, "NONCE_RESULT=") {
			t.Fatalf("child %d failed: %s", index, result)
		}
	}
	return results
}

func assertPublishedPrimitiveWinner(t *testing.T, directoryPath, label string) {
	t.Helper()
	entries, err := os.ReadDir(directoryPath)
	if err != nil || len(entries) != 1 || entries[0].Name() != ReservedRecordName {
		t.Fatalf("primitive inventory=%v err=%v", entries, err)
	}
	path := filepath.Join(directoryPath, ReservedRecordName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected, _, err := reservationRecordBytes(nonceStoreFields(label))
	if err != nil || !bytes.Equal(data, expected) {
		t.Fatalf("primitive winner bytes mismatch err=%v", err)
	}
	snapshot, err := Parse(map[string][]byte{ReservedRecordName: data})
	if err != nil || snapshot.State() != Reserved {
		t.Fatalf("primitive winner parse=%+v err=%v", snapshot, err)
	}
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&07777 != 0400 ||
		stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > MaxRecordBytes {
		t.Fatalf("primitive winner identity=%+v err=%v", stat, err)
	}
}

func countNonceResults(results []string) map[string]int {
	counts := make(map[string]int)
	for _, result := range results {
		for _, value := range []string{"created", "recovered", "conflict"} {
			if strings.Contains(result, "NONCE_RESULT="+value) {
				counts[value]++
			}
		}
	}
	return counts
}

func assertProcessNonceWinner(t *testing.T, rootPath, label string) {
	t.Helper()
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	store, err := openRetainedStore(root, nonceStoreHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	fields := nonceStoreFields(label)
	session, err := store.openExisting(fields.NonceID, fields.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	snapshot, err := session.inspect()
	if err != nil || snapshot.State() != Reserved || snapshot.NonceID() != fields.NonceID || snapshot.TransactionID() != fields.TransactionID {
		t.Fatalf("winner snapshot=%+v err=%v", snapshot, err)
	}
	rootEntries, err := os.ReadDir(rootPath)
	if err != nil || len(rootEntries) != 1 || rootEntries[0].Name() != nonceDirectoryName(fields.NonceID, fields.TransactionID) {
		t.Fatalf("root inventory=%v err=%v", rootEntries, err)
	}
	recordPath := filepath.Join(rootPath, rootEntries[0].Name(), ReservedRecordName)
	info, err := os.Stat(recordPath)
	if err != nil || info.Mode().Perm() != 0400 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxRecordBytes {
		t.Fatalf("record metadata=%v err=%v", info, err)
	}
	var stat syscall.Stat_t
	if err := syscall.Stat(recordPath, &stat); err != nil || stat.Nlink != 1 || stat.Uid != 0 || stat.Gid != 0 {
		t.Fatalf("record identity=%+v err=%v", stat, err)
	}
}
