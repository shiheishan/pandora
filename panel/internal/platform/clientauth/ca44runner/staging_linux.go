//go:build linux

package ca44runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	lockName            = ".ca44-root-runner.lock"
	artifactName        = "devices.classified.json"
	detachedName        = "classifier.detached.json"
	expectationsName    = "release.expectations.json"
	releaseManifestName = "signed.release-manifest"
	receiptName         = "verifier.receipt.json"
)

type privateStage struct {
	trustFD      int
	stagingFD    int
	activeFD     int
	quarantineFD int
	publishFD    int
	lockFD       int
	runFD        int
	candidateFD  int
	bundleFD     int
	runName      string
	published    bool
}

func openTrustedRoot(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.Contains(path, "//") {
		return -1, errors.New("trust root path is not canonical")
	}
	parts, err := splitRelativeLinux(strings.TrimPrefix(path, "/"))
	if err != nil || len(parts) == 0 {
		return -1, errors.New("trust root path is invalid")
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	if err := validateTrustedDirectory(current, false, 0); err != nil {
		unix.Close(current)
		return -1, err
	}
	for _, part := range parts {
		next, err := openDirectoryComponent(current, part)
		unix.Close(current)
		if err != nil {
			return -1, err
		}
		current = next
		if err := validateTrustedDirectory(current, false, 0); err != nil {
			unix.Close(current)
			return -1, err
		}
	}
	return current, nil
}

func openDirectoryComponent(parentFD int, name string) (int, error) {
	if !validComponent(name) {
		return -1, errors.New("invalid directory component")
	}
	return unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS,
	})
}

func openRelativeDirectory(rootFD int, relative string, exactPrivate bool, expectedDev uint64) (int, error) {
	parts, err := splitRelativeLinux(relative)
	if err != nil {
		return -1, err
	}
	current, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for index, part := range parts {
		next, openErr := openDirectoryComponent(current, part)
		unix.Close(current)
		if openErr != nil {
			return -1, openErr
		}
		current = next
		private := exactPrivate && index == len(parts)-1
		if err := validateTrustedDirectory(current, private, expectedDev); err != nil {
			unix.Close(current)
			return -1, err
		}
	}
	return current, nil
}

func validateTrustedDirectory(fd int, exactPrivate bool, expectedDev uint64) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Nlink < 1 {
		return errors.New("untrusted directory identity")
	}
	mode := stat.Mode & 0o7777
	if exactPrivate {
		if mode != 0o700 {
			return errors.New("private directory mode mismatch")
		}
	} else if mode&0o022 != 0 {
		return errors.New("trusted directory is group or world writable")
	}
	if expectedDev != 0 && uint64(stat.Dev) != expectedDev {
		return errors.New("directory device mismatch")
	}
	return nil
}

func ensurePrivateDirectory(parentFD int, name string, expectedDev uint64) (int, error) {
	if !validComponent(name) {
		return -1, errors.New("invalid private directory name")
	}
	created := false
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	} else {
		created = true
	}
	fd, err := openDirectoryComponent(parentFD, name)
	if err != nil {
		return -1, err
	}
	if created {
		if err := unix.Fchmod(fd, 0o700); err != nil {
			unix.Close(fd)
			return -1, err
		}
	}
	if err := validateTrustedDirectory(fd, true, expectedDev); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func newPrivateStage(trustRoot, stagingRoot, publishRoot string) (*privateStage, error) {
	stage := &privateStage{trustFD: -1, stagingFD: -1, activeFD: -1, quarantineFD: -1,
		publishFD: -1, lockFD: -1, runFD: -1, candidateFD: -1, bundleFD: -1}
	var err error
	stage.trustFD, err = openTrustedRoot(trustRoot)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*privateStage, error) {
		if stage.runName != "" && stage.activeFD >= 0 && stage.quarantineFD >= 0 {
			if quarantineErr := stage.quarantine(); quarantineErr != nil {
				err = errors.Join(err, quarantineErr)
			}
		}
		stage.close()
		return nil, err
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(stage.trustFD, &rootStat); err != nil {
		return fail(err)
	}
	stage.stagingFD, err = openRelativeDirectory(stage.trustFD, stagingRoot, true, uint64(rootStat.Dev))
	if err != nil {
		return fail(err)
	}
	var stagingStat unix.Stat_t
	if err := unix.Fstat(stage.stagingFD, &stagingStat); err != nil {
		return fail(err)
	}
	stage.publishFD, err = openRelativeDirectory(stage.trustFD, publishRoot, true, uint64(stagingStat.Dev))
	if err != nil {
		return fail(err)
	}
	stage.activeFD, err = ensurePrivateDirectory(stage.stagingFD, "active", uint64(stagingStat.Dev))
	if err != nil {
		return fail(err)
	}
	stage.quarantineFD, err = ensurePrivateDirectory(stage.stagingFD, "quarantine", uint64(stagingStat.Dev))
	if err != nil {
		return fail(err)
	}
	stage.lockFD, err = unix.Openat(stage.stagingFD, lockName,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err == nil {
		if err = unix.Fchmod(stage.lockFD, 0o600); err != nil {
			return fail(err)
		}
	} else if errors.Is(err, unix.EEXIST) {
		stage.lockFD, err = unix.Openat(stage.stagingFD, lockName,
			unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return fail(err)
	}
	if err := validateLockFile(stage.lockFD, uint64(stagingStat.Dev)); err != nil {
		return fail(err)
	}
	if err := unix.Flock(stage.lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(err)
	}
	if err := stage.scavengeActive(); err != nil {
		return fail(err)
	}
	newRunName, err := randomRunName()
	if err != nil {
		return fail(err)
	}
	if err := unix.Mkdirat(stage.activeFD, newRunName, 0o700); err != nil {
		return fail(err)
	}
	stage.runName = newRunName
	stage.runFD, err = openDirectoryComponent(stage.activeFD, stage.runName)
	if err != nil {
		return fail(err)
	}
	if err := validateTrustedDirectory(stage.runFD, true, uint64(stagingStat.Dev)); err != nil {
		return fail(err)
	}
	stage.candidateFD, err = ensurePrivateDirectory(stage.runFD, "candidate", uint64(stagingStat.Dev))
	if err != nil {
		return fail(err)
	}
	stage.bundleFD, err = ensurePrivateDirectory(stage.runFD, "bundle", uint64(stagingStat.Dev))
	if err != nil {
		return fail(err)
	}
	if err := fsyncDirectories(stage.runFD, stage.activeFD, stage.stagingFD); err != nil {
		return fail(err)
	}
	return stage, nil
}

func validateLockFile(fd int, expectedDev uint64) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 || stat.Nlink != 1 ||
		stat.Mode&0o7777 != 0o600 || uint64(stat.Dev) != expectedDev {
		return errors.New("lock file identity mismatch")
	}
	return nil
}

func (s *privateStage) scavengeActive() error {
	dupFD, err := duplicateCloseOnExec(s.activeFD)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(dupFD), "ca44-active")
	if dir == nil {
		unix.Close(dupFD)
		return errors.New("cannot wrap active directory")
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	var stagingStat unix.Stat_t
	if err := unix.Fstat(s.stagingFD, &stagingStat); err != nil {
		return err
	}
	for _, name := range names {
		if !validRunName(name) {
			return errors.New("unexpected active entry")
		}
		fd, err := openDirectoryComponent(s.activeFD, name)
		if err != nil {
			return err
		}
		err = validateTrustedDirectory(fd, true, uint64(stagingStat.Dev))
		unix.Close(fd)
		if err != nil {
			return err
		}
		if err := unix.Renameat2(s.activeFD, name, s.quarantineFD, name, unix.RENAME_NOREPLACE); err != nil {
			return err
		}
	}
	return fsyncDirectories(s.activeFD, s.quarantineFD, s.stagingFD)
}

func createExactFileAt(dirFD int, name string, data []byte, mode uint32) (*os.File, error) {
	if !validComponent(name) || len(data) == 0 {
		return nil, errors.New("invalid private file")
	}
	fd, err := unix.Openat(dirFD, name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("cannot wrap private file")
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		file.Close()
		_ = unix.Unlinkat(dirFD, name, 0)
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		file.Close()
		_ = unix.Unlinkat(dirFD, name, 0)
		return nil, err
	}
	written := 0
	for written < len(data) {
		n, err := file.Write(data[written:])
		if err != nil {
			return fail(err)
		}
		if n <= 0 {
			return fail(io.ErrShortWrite)
		}
		written += n
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fail(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 || stat.Nlink != 1 ||
		stat.Mode&0o7777 != mode || stat.Size != int64(len(data)) {
		return fail(errors.New("private file identity mismatch"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	if err := fsyncDirectories(dirFD); err != nil {
		return fail(err)
	}
	return file, nil
}

func (s *privateStage) publishBundle(releaseID string) error {
	if !releaseIDPattern.MatchString(releaseID) || s.published {
		return errors.New("invalid publish transition")
	}
	if err := unix.Renameat2(s.runFD, "bundle", s.publishFD, releaseID, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	s.published = true
	return fsyncDirectories(s.publishFD, s.runFD, s.activeFD, s.stagingFD)
}

func (s *privateStage) quarantine() error {
	if s == nil || s.runName == "" || s.published {
		return nil
	}
	if err := unix.Renameat2(s.activeFD, s.runName, s.quarantineFD, s.runName, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	s.runName = ""
	return fsyncDirectories(s.activeFD, s.quarantineFD, s.stagingFD)
}

func (s *privateStage) cleanupSuccess() error {
	if s == nil || !s.published || s.runName == "" {
		return errors.New("invalid successful cleanup")
	}
	if err := unix.Unlinkat(s.runFD, "candidate", unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if err := fsyncDirectories(s.runFD); err != nil {
		return err
	}
	if err := unix.Unlinkat(s.activeFD, s.runName, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	s.runName = ""
	return fsyncDirectories(s.activeFD, s.stagingFD)
}

func (s *privateStage) existingBundleMatches(
	manifest Manifest,
	manifestBytes, detached, expectations, receipt []byte,
) error {
	var publishStat unix.Stat_t
	if err := unix.Fstat(s.publishFD, &publishStat); err != nil {
		return err
	}
	bundleFD, err := openDirectoryComponent(s.publishFD, manifest.ReleaseID)
	if err != nil {
		return err
	}
	defer unix.Close(bundleFD)
	if err := validateTrustedDirectory(bundleFD, true, uint64(publishStat.Dev)); err != nil {
		return err
	}
	dupFD, err := duplicateCloseOnExec(bundleFD)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(dupFD), "ca44-existing-bundle")
	if dir == nil {
		unix.Close(dupFD)
		return errors.New("cannot wrap existing bundle")
	}
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return err
	}
	sort.Strings(names)
	wantNames := []string{artifactName, detachedName, expectationsName, releaseManifestName, receiptName}
	sort.Strings(wantNames)
	if len(names) != len(wantNames) {
		return errors.New("existing bundle entry count mismatch")
	}
	for index := range names {
		if names[index] != wantNames[index] {
			return errors.New("existing bundle entry mismatch")
		}
	}
	type expectedFile struct {
		name string
		data []byte
		hash string
		max  int64
	}
	files := []expectedFile{
		{name: artifactName, hash: manifest.ArtifactSHA256, max: int64(manifest.ArtifactLength)},
		{name: detachedName, data: detached, hash: digestHex(detached), max: MaxEnvelopeBytes},
		{name: expectationsName, data: expectations, hash: digestHex(expectations), max: MaxEnvelopeBytes},
		{name: releaseManifestName, data: manifestBytes, hash: digestHex(manifestBytes), max: MaxManifestBytes},
		{name: receiptName, data: receipt, hash: digestHex(receipt), max: MaxEnvelopeBytes},
	}
	for _, expected := range files {
		file, identity, err := OpenTrustedRegularAt(bundleFD, expected.name,
			expected.hash, expected.max, 0o600, 0)
		if err != nil {
			return err
		}
		if expected.name == artifactName {
			err = requireExactSize(file, int64(manifest.ArtifactLength))
		} else {
			var actual []byte
			actual, err = readRetainedFile(file, expected.max)
			if err == nil && !byteEqual(actual, expected.data) {
				err = errors.New("existing bundle content mismatch")
			}
		}
		if err == nil {
			err = VerifySameFile(file, identity, 0)
		}
		file.Close()
		if err != nil {
			return err
		}
	}
	// A prior rename may have succeeded while the publish-parent fsync failed.
	// Exact byte equality is insufficient for an idempotent success receipt:
	// retry the directory durability barrier and fail closed if it cannot be
	// proven now.
	return syncVerifiedExistingBundle(bundleFD, s.publishFD, fsyncDirectories)
}

func (s *privateStage) discardVerifiedDuplicate() error {
	if s == nil || s.runName == "" || s.published {
		return errors.New("invalid duplicate cleanup")
	}
	for _, name := range []string{artifactName, detachedName, expectationsName, releaseManifestName, receiptName} {
		if err := unix.Unlinkat(s.bundleFD, name, 0); err != nil {
			return err
		}
	}
	if err := fsyncDirectories(s.bundleFD); err != nil {
		return err
	}
	if err := unix.Unlinkat(s.runFD, "bundle", unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if err := unix.Unlinkat(s.runFD, "candidate", unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if err := fsyncDirectories(s.runFD); err != nil {
		return err
	}
	if err := unix.Unlinkat(s.activeFD, s.runName, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	s.runName = ""
	return fsyncDirectories(s.activeFD, s.stagingFD)
}

func digestHex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func (s *privateStage) close() {
	if s == nil {
		return
	}
	for _, fd := range []int{s.bundleFD, s.candidateFD, s.runFD, s.publishFD,
		s.quarantineFD, s.activeFD, s.lockFD, s.stagingFD, s.trustFD} {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}
	s.bundleFD, s.candidateFD, s.runFD, s.publishFD = -1, -1, -1, -1
	s.quarantineFD, s.activeFD, s.lockFD, s.stagingFD, s.trustFD = -1, -1, -1, -1, -1
}

func fsyncDirectories(fds ...int) error {
	for _, fd := range fds {
		if fd < 0 {
			continue
		}
		if err := unix.Fsync(fd); err != nil {
			return fmt.Errorf("fsync directory: %w", err)
		}
	}
	return nil
}

func duplicateCloseOnExec(fd int) (int, error) {
	if fd < 0 {
		return -1, errors.New("invalid descriptor duplication")
	}
	return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
}
