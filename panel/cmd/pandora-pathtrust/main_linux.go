//go:build linux && (amd64 || arm64)

// pandora-pathtrust verifies release artifacts without following pathname
// symlinks. It is intentionally Linux-only because its security contract
// depends on openat2(2)/openat(2), stable directory file descriptors, and
// Linux stat metadata.
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	exitUsage  = 64
	exitDenied = 65
	exitSystem = 70

	linuxOPath      = 0x200000
	linuxODirectory = 0x10000
	linuxONoFollow  = 0x20000
	linuxOCloExec   = 0x80000

	linuxSYSOpenat2       = 437 // Linux asm-generic, amd64 and arm64.
	resolveNoMagicLinks   = 0x02
	resolveNoSymlinks     = 0x04
	resolveBeneath        = 0x08
	trustedArtifactFD     = 3
	trustedArtifactFDPath = "/proc/self/fd/3"
)

type openHow struct {
	Flags   uint64
	Mode    uint64
	Resolve uint64
}

type policyError struct {
	reason string
	err    error
}

func (e *policyError) Error() string {
	if e.err == nil {
		return e.reason
	}
	return e.reason + ": " + e.err.Error()
}

func (e *policyError) Unwrap() error { return e.err }

type verifiedArtifact struct {
	fd          int
	path        string
	sha256      string
	chainSHA256 string
	stat        syscall.Stat_t
	openMode    string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return exitUsage
	}

	switch args[0] {
	case "check":
		return runCheck(args[1:])
	case "exec":
		return runExec(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "pandora-pathtrust: unknown command %q\n", args[0])
		printUsage()
		return exitUsage
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  pandora-pathtrust check --path ABSOLUTE_PATH [--expect-sha256 HEX] [--expect-mode OCTAL] [--expect-device DECIMAL] [--allow-devices CSV] [--expect-chain-sha256 HEX]")
	fmt.Fprintln(os.Stderr, "  pandora-pathtrust exec  --path ABSOLUTE_PATH --expect-sha256 HEX --expect-mode 0500 --expect-device DECIMAL --allow-devices CSV --expect-chain-sha256 HEX -- [ARG ...]")
	fmt.Fprintln(os.Stderr, "exit codes: 0=trusted, 64=usage, 65=trust denied, 70=system failure; exec propagates child exit")
}

func newFlagSet(name string) (*flag.FlagSet, *string, *string, *string, *string, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	path := fs.String("path", "", "absolute path to a root-owned regular artifact")
	expected := fs.String("expect-sha256", "", "exact lowercase/uppercase SHA-256")
	expectedMode := fs.String("expect-mode", "", "exact octal permission bits, for example 0500")
	expectedDevice := fs.String("expect-device", "", "exact decimal st_dev from the trusted release manifest")
	allowedDevices := fs.String("allow-devices", "", "sorted comma-separated ancestor/target st_dev allowlist")
	expectedChain := fs.String("expect-chain-sha256", "", "exact SHA-256 of the ordered ancestor dev/inode chain")
	return fs, path, expected, expectedMode, expectedDevice, allowedDevices, expectedChain
}

func runCheck(args []string) int {
	fs, path, expected, expectedMode, expectedDevice, allowedDevices, expectedChain := newFlagSet("check")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *path == "" {
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: check requires --path and accepts no positional arguments")
		return exitUsage
	}

	artifact, code := verifyForCLI(*path, *expected, *expectedMode, *expectedDevice, *allowedDevices, *expectedChain)
	if artifact == nil {
		return code
	}
	defer syscall.Close(artifact.fd)

	fmt.Printf(
		"PANDORA_PATHTRUST_V1 status=trusted path_hex=%s sha256=%s chain_sha256=%s uid=%d mode=%04o nlink=%d dev=%d ino=%d traversal=%s fd_scope=process\n",
		hex.EncodeToString([]byte(artifact.path)),
		artifact.sha256,
		artifact.chainSHA256,
		artifact.stat.Uid,
		artifact.stat.Mode&07777,
		artifact.stat.Nlink,
		artifact.stat.Dev,
		artifact.stat.Ino,
		artifact.openMode,
	)
	return 0
}

func runExec(args []string) int {
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: exec requires -- before target arguments")
		return exitUsage
	}

	fs, path, expected, expectedMode, expectedDevice, allowedDevices, expectedChain := newFlagSet("exec")
	if err := fs.Parse(args[:separator]); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *path == "" {
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: exec requires --path")
		return exitUsage
	}
	if *expected == "" || *expectedMode != "0500" || *expectedDevice == "" ||
		*allowedDevices == "" || *expectedChain == "" {
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: exec requires --expect-sha256, --expect-mode 0500, --expect-device, --allow-devices, and --expect-chain-sha256")
		return exitUsage
	}

	artifact, code := verifyForCLI(*path, *expected, *expectedMode, *expectedDevice, *allowedDevices, *expectedChain)
	if artifact == nil {
		return code
	}
	if artifact.openMode != "openat2" {
		syscall.Close(artifact.fd)
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: openat2_required_for_exec")
		return exitDenied
	}

	// ExtraFiles maps the already-verified open file description to fd 3 in the
	// child. Executing /proc/self/fd/3 keeps the kernel's executable selection
	// bound to that descriptor, rather than reopening the attacker-raceable
	// pathname after verification.
	targetFile := os.NewFile(uintptr(artifact.fd), "pandora-trusted-artifact")
	if targetFile == nil {
		syscall.Close(artifact.fd)
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: system failure: cannot wrap trusted fd")
		return exitSystem
	}
	defer targetFile.Close()

	commandArgs := append([]string{trustedArtifactFDPath}, args[separator+1:]...)
	cmd := exec.Command("/bin/bash", commandArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(trustedChildEnv(os.Environ()),
		fmt.Sprintf("PANDORA_TRUSTED_FD=%d", trustedArtifactFD),
		"PANDORA_PATHTRUST_VERSION=1",
		"PANDORA_TRUSTED_SHA256="+artifact.sha256,
		"PANDORA_TRUSTED_CHAIN_SHA256="+artifact.chainSHA256,
		fmt.Sprintf("PANDORA_TRUSTED_DEVICE=%d", artifact.stat.Dev),
		fmt.Sprintf("PANDORA_TRUSTED_MODE=%04o", artifact.stat.Mode&07777),
	)
	cmd.ExtraFiles = []*os.File{targetFile}
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "pandora-pathtrust: child execution failure: %v\n", err)
	return exitSystem
}

func trustedChildEnv(source []string) []string {
	const clientAuthPrefix = "PANDORA_CLIENT_AUTH_00043_"
	filtered := []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/nonexistent",
		"LANG=C",
		"LC_ALL=C",
	}
	for _, item := range source {
		if strings.HasPrefix(item, clientAuthPrefix) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func verifyForCLI(path, expected, expectedMode, expectedDevice, allowedDeviceCSV, expectedChain string) (*verifiedArtifact, int) {
	if expected != "" {
		if len(expected) != sha256.Size*2 {
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: expected_sha256_invalid")
			return nil, exitDenied
		}
		if _, err := hex.DecodeString(expected); err != nil {
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: expected_sha256_invalid")
			return nil, exitDenied
		}
	}
	if expectedChain != "" {
		if len(expectedChain) != sha256.Size*2 {
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: expected_chain_sha256_invalid")
			return nil, exitDenied
		}
		if _, err := hex.DecodeString(expectedChain); err != nil {
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: expected_chain_sha256_invalid")
			return nil, exitDenied
		}
	}
	var modeValue uint64
	if expectedMode != "" {
		var err error
		modeValue, err = strconv.ParseUint(expectedMode, 8, 12)
		if err != nil || len(expectedMode) != 4 {
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: expected_mode_invalid")
			return nil, exitDenied
		}
	}
	var deviceValue uint64
	if expectedDevice != "" {
		var err error
		deviceValue, err = strconv.ParseUint(expectedDevice, 10, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: expected_device_invalid")
			return nil, exitDenied
		}
	}
	allowedDevices, err := parseAllowedDevices(allowedDeviceCSV)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: allowed_devices_invalid")
		return nil, exitDenied
	}
	if expectedDevice != "" {
		if _, ok := allowedDevices[deviceValue]; len(allowedDevices) > 0 && !ok {
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: expected_device_not_allowed")
			return nil, exitDenied
		}
	}

	artifact, err := verifyArtifact(path, allowedDevices)
	if err != nil {
		var denied *policyError
		if errors.As(err, &denied) {
			fmt.Fprintf(os.Stderr, "pandora-pathtrust: trust denied: %s\n", denied)
			return nil, exitDenied
		}
		fmt.Fprintf(os.Stderr, "pandora-pathtrust: system failure: %v\n", err)
		return nil, exitSystem
	}

	if expected != "" {
		gotBytes, _ := hex.DecodeString(artifact.sha256)
		expectedBytes, _ := hex.DecodeString(expected)
		if subtle.ConstantTimeCompare(gotBytes, expectedBytes) != 1 {
			syscall.Close(artifact.fd)
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: sha256_mismatch")
			return nil, exitDenied
		}
	}
	if expectedMode != "" && uint64(artifact.stat.Mode&07777) != modeValue {
		syscall.Close(artifact.fd)
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: mode_mismatch")
		return nil, exitDenied
	}
	if expectedDevice != "" && uint64(artifact.stat.Dev) != deviceValue {
		syscall.Close(artifact.fd)
		fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: device_mismatch")
		return nil, exitDenied
	}
	if expectedChain != "" {
		gotBytes, _ := hex.DecodeString(artifact.chainSHA256)
		expectedBytes, _ := hex.DecodeString(expectedChain)
		if subtle.ConstantTimeCompare(gotBytes, expectedBytes) != 1 {
			syscall.Close(artifact.fd)
			fmt.Fprintln(os.Stderr, "pandora-pathtrust: trust denied: chain_sha256_mismatch")
			return nil, exitDenied
		}
	}
	return artifact, 0
}

func parseAllowedDevices(csv string) (map[uint64]struct{}, error) {
	allowed := make(map[uint64]struct{})
	if csv == "" {
		return allowed, nil
	}
	var previous uint64
	for index, raw := range strings.Split(csv, ",") {
		if raw == "" {
			return nil, errors.New("empty device")
		}
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || (index > 0 && value <= previous) {
			return nil, errors.New("device allowlist is not strictly sorted")
		}
		allowed[value] = struct{}{}
		previous = value
	}
	return allowed, nil
}

func verifyArtifact(path string, allowedDevices map[uint64]struct{}) (*verifiedArtifact, error) {
	if !filepath.IsAbs(path) {
		return nil, &policyError{reason: "path_not_absolute"}
	}
	if filepath.Clean(path) != path || strings.Contains(path, "//") {
		return nil, &policyError{reason: "path_not_canonical"}
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(components) == 0 || (len(components) == 1 && components[0] == "") {
		return nil, &policyError{reason: "target_is_root"}
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, &policyError{reason: "path_component_invalid"}
		}
	}

	rootFD, err := syscall.Open("/", linuxOPath|linuxODirectory|linuxONoFollow|linuxOCloExec, 0)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	currentFD := rootFD
	chainHasher := sha256.New()
	keepCurrent := false
	defer func() {
		if !keepCurrent {
			syscall.Close(currentFD)
		}
	}()

	if err := validateDirectoryFD(currentFD, allowedDevices); err != nil {
		return nil, err
	}
	if err := appendChainRecord(chainHasher, 0, "directory", currentFD); err != nil {
		return nil, err
	}

	traversal := "openat2"
	for index, component := range components {
		isTarget := index == len(components)-1
		flags := linuxOPath | linuxODirectory | linuxONoFollow | linuxOCloExec
		if isTarget {
			flags = syscall.O_RDONLY | syscall.O_NONBLOCK | linuxONoFollow | linuxOCloExec
		}

		nextFD, method, err := openChild(currentFD, component, flags)
		if err != nil {
			if errors.Is(err, syscall.ELOOP) {
				return nil, &policyError{reason: "symlink_refused", err: err}
			}
			return nil, &policyError{reason: "component_open_failed", err: err}
		}
		if method == "openat" {
			traversal = "openat"
		}
		syscall.Close(currentFD)
		currentFD = nextFD

		if isTarget {
			if err := validateArtifactFD(currentFD, allowedDevices); err != nil {
				return nil, err
			}
		} else if err := validateDirectoryFD(currentFD, allowedDevices); err != nil {
			return nil, err
		}
		kind := "directory"
		if isTarget {
			kind = "artifact"
		}
		if err := appendChainRecord(chainHasher, index+1, kind, currentFD); err != nil {
			return nil, err
		}
	}

	var stat syscall.Stat_t
	if err := syscall.Fstat(currentFD, &stat); err != nil {
		return nil, fmt.Errorf("fstat verified artifact: %w", err)
	}
	digest, err := sha256FD(currentFD)
	if err != nil {
		return nil, fmt.Errorf("hash verified artifact: %w", err)
	}
	var postHashStat syscall.Stat_t
	if err := syscall.Fstat(currentFD, &postHashStat); err != nil {
		return nil, fmt.Errorf("post-hash fstat verified artifact: %w", err)
	}
	if !sameArtifactStat(stat, postHashStat) {
		return nil, &policyError{reason: "target_changed_while_hashing"}
	}

	keepCurrent = true
	return &verifiedArtifact{
		fd:          currentFD,
		path:        path,
		sha256:      digest,
		chainSHA256: hex.EncodeToString(chainHasher.Sum(nil)),
		stat:        stat,
		openMode:    traversal,
	}, nil
}

func appendChainRecord(writer io.Writer, index int, kind string, fd int) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("fstat path chain: %w", err)
	}
	_, err := fmt.Fprintf(
		writer,
		"%d|%s|%d|%d|%d|%04o|%d\n",
		index,
		kind,
		stat.Dev,
		stat.Ino,
		stat.Uid,
		stat.Mode&07777,
		stat.Nlink,
	)
	return err
}

func sameArtifactStat(before, after syscall.Stat_t) bool {
	return before.Dev == after.Dev &&
		before.Ino == after.Ino &&
		before.Mode == after.Mode &&
		before.Uid == after.Uid &&
		before.Gid == after.Gid &&
		before.Nlink == after.Nlink &&
		before.Size == after.Size &&
		before.Mtim == after.Mtim &&
		before.Ctim == after.Ctim
}

func openChild(parentFD int, name string, flags int) (int, string, error) {
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return -1, "", err
	}
	how := openHow{
		Flags:   uint64(flags),
		Resolve: resolveBeneath | resolveNoSymlinks | resolveNoMagicLinks,
	}
	fd, _, errno := syscall.Syscall6(
		linuxSYSOpenat2,
		uintptr(parentFD),
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&how)),
		unsafe.Sizeof(how),
		0,
		0,
	)
	if errno == 0 {
		return int(fd), "openat2", nil
	}

	// A retained directory fd plus O_NOFOLLOW on every single path component
	// is the old-kernel fallback. It still prevents a rename from redirecting a
	// later component lookup, because each lookup is relative to the already
	// opened directory inode. Other openat2 failures are policy failures.
	if errno != syscall.ENOSYS && errno != syscall.EINVAL {
		return -1, "", errno
	}
	fallbackFD, fallbackErr := syscall.Openat(parentFD, name, flags, 0)
	if fallbackErr != nil {
		return -1, "", fallbackErr
	}
	return fallbackFD, "openat", nil
}

func validateDirectoryFD(fd int, allowedDevices map[uint64]struct{}) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("fstat ancestor: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return &policyError{reason: "ancestor_not_directory"}
	}
	if stat.Uid != 0 {
		return &policyError{reason: "ancestor_not_root_owned"}
	}
	if stat.Mode&0022 != 0 {
		return &policyError{reason: "ancestor_group_or_world_writable"}
	}
	if _, ok := allowedDevices[uint64(stat.Dev)]; len(allowedDevices) > 0 && !ok {
		return &policyError{reason: "ancestor_device_not_allowed"}
	}
	return nil
}

func validateArtifactFD(fd int, allowedDevices map[uint64]struct{}) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("fstat target: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return &policyError{reason: "target_not_regular"}
	}
	if stat.Uid != 0 {
		return &policyError{reason: "target_not_root_owned"}
	}
	if stat.Mode&0022 != 0 {
		return &policyError{reason: "target_group_or_world_writable"}
	}
	if stat.Nlink != 1 {
		return &policyError{reason: "target_link_count_not_one"}
	}
	if _, ok := allowedDevices[uint64(stat.Dev)]; len(allowedDevices) > 0 && !ok {
		return &policyError{reason: "target_device_not_allowed"}
	}
	return nil
}

func sha256FD(fd int) (string, error) {
	duplicateFD, err := syscall.Dup(fd)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(duplicateFD), "pandora-pathtrust-hash")
	if file == nil {
		syscall.Close(duplicateFD)
		return "", errors.New("cannot wrap duplicate fd")
	}
	defer file.Close()

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
