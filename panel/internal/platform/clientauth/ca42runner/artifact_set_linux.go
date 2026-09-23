//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsule"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"golang.org/x/sys/unix"
)

const (
	maxPathtrustBytes       = 128 << 20
	maxAttestationCoreBytes = 4 << 20
	maxVerifierBytes        = 128 << 20
	maxPreflightBytes       = 4 << 20
	maxMigrationRunnerBytes = 16 << 20
	maxGooseBytes           = 256 << 20
	maxMigrationSQLBytes    = 16 << 20
	maxGlobalsDumpBytes     = int64(1 << 30)
	maxDatabaseDumpBytes    = int64(1 << 40)
)

type artifactKind uint8

const (
	artifactData artifactKind = iota
	artifactELF
	artifactBash
	artifactEd25519PublicKey
	artifactPGDump
)

type retainedArtifact struct {
	file             *os.File
	parent           *os.File
	relativeName     string
	absolutePath     string
	identity         retainedIdentity
	digest           [sha256.Size]byte
	maxBytes         int64
	exactMode        uint32
	expectedDevice   uint64
	expectedChainHex string
	kind             artifactKind
	architecture     string
	owned            bool
}

// RetainedArtifactSet owns every newly opened CA42 artifact descriptor. The
// trust-capsule and external-manifest entries deliberately alias the exact
// descriptors already retained by VerificationSession and are not closed here.
// No descriptor is exposed through the production API.
type RetainedArtifactSet struct {
	mu                 sync.Mutex
	attemptRoot        *os.File
	attemptID          string
	attemptIdentity    retainedIdentity
	migrationDirectory *os.File
	migrationIdentity  retainedIdentity
	artifacts          []*retainedArtifact
	migrations         []*retainedArtifact
	clientAuth00042    *retainedArtifact
	trustCapsule       *retainedArtifact
	externalManifest   *retainedArtifact
	byName             map[string]*retainedArtifact
	plan               ca42execution.Plan
	capsule            ca42capsule.Capsule
	manifestReceipt    ca42manifest.Receipt
	attestationWindow  attestationValidity
	closed             bool
}

type artifactSpec struct {
	name, expectedSHA256, expectedChainSHA256 string
	maxBytes                                  int64
	mode                                      uint32
	device                                    uint64
	kind                                      artifactKind
}

func openRetainedArtifactSet(
	ctx context.Context,
	attemptRoot *os.File,
	attemptID string,
	plan ca42execution.Plan,
	capsule ca42capsule.Capsule,
	trustCapsuleFile, externalManifestFile *os.File,
	now time.Time,
) (*RetainedArtifactSet, error) {
	if ctx == nil || attemptRoot == nil || trustCapsuleFile == nil || externalManifestFile == nil ||
		!attemptIDPattern.MatchString(attemptID) || plan.AttemptID != attemptID {
		return nil, errors.New("CA42 retained artifact set input invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	originalIdentity, err := retainedStat(int(attemptRoot.Fd()))
	if err != nil || originalIdentity.mode&unix.S_IFMT != unix.S_IFDIR || originalIdentity.uid != 0 ||
		originalIdentity.mode&0o7777 != 0o700 || originalIdentity.nlink < 1 {
		return nil, errors.New("CA42 retained attempt root identity denied")
	}
	duplicateFD, err := unix.FcntlInt(attemptRoot.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	duplicate := os.NewFile(uintptr(duplicateFD), "ca42-retained-attempt-root")
	if duplicate == nil {
		unix.Close(duplicateFD)
		return nil, errors.New("CA42 retained attempt root descriptor conversion failed")
	}
	set := &RetainedArtifactSet{
		attemptRoot: duplicate, attemptID: attemptID, attemptIdentity: originalIdentity,
		byName: make(map[string]*retainedArtifact), plan: plan, capsule: capsule,
	}
	failed := true
	defer func() {
		if failed {
			_ = set.Close()
		}
	}()
	if duplicatedIdentity, statErr := retainedStat(duplicateFD); statErr != nil || duplicatedIdentity != originalIdentity {
		return nil, errors.New("CA42 duplicated attempt root identity mismatch")
	}
	if err := set.validateAttemptBinding(); err != nil {
		return nil, err
	}

	registerExisting := func(name, expected string, maxBytes int64, file *os.File) (*retainedArtifact, error) {
		artifact, openErr := inspectExistingRetainedArtifact(ctx, duplicate, file, name, maxBytes, 0o400, expected, originalIdentity.dev)
		if openErr != nil {
			return nil, openErr
		}
		if registerErr := set.register(artifact); registerErr != nil {
			return nil, registerErr
		}
		return artifact, nil
	}
	set.trustCapsule, err = registerExisting(TrustCapsuleName, plan.TrustCapsuleSHA256, ca42capsule.MaxCapsuleBytes, trustCapsuleFile)
	if err != nil {
		return nil, err
	}
	set.externalManifest, err = registerExisting(ExternalManifestName, plan.ExternalManifestSHA256, ca42manifest.MaxManifestBytes, externalManifestFile)
	if err != nil {
		return nil, err
	}

	specs := []artifactSpec{
		{name: PathtrustBinaryName, expectedSHA256: plan.PathtrustBinarySHA256, expectedChainSHA256: plan.PathtrustChainSHA256, maxBytes: maxPathtrustBytes, mode: 0o500, device: plan.PathtrustDevice, kind: artifactELF},
		{name: AttestationCoreName, expectedSHA256: plan.AttestationCoreSHA256, expectedChainSHA256: plan.AttestationCoreChainSHA256, maxBytes: maxAttestationCoreBytes, mode: 0o500, device: plan.AttestationCoreDevice, kind: artifactBash},
		{name: AttestationName, expectedSHA256: plan.AttestationSHA256, maxBytes: maxAttestationBytes, mode: 0o400, device: originalIdentity.dev, kind: artifactData},
		{name: AttestationExpectedName, expectedSHA256: plan.ExpectedSHA256, maxBytes: maxExpectedBytes, mode: 0o400, device: originalIdentity.dev, kind: artifactData},
		{name: AttestationPublicKeyName, expectedSHA256: plan.AttestationPublicKeySHA256, maxBytes: maxPublicKeyBytes, mode: 0o400, device: originalIdentity.dev, kind: artifactEd25519PublicKey},
		{name: ManifestVerifierName, expectedSHA256: plan.ManifestVerifierSHA256, maxBytes: maxVerifierBytes, mode: 0o500, device: originalIdentity.dev, kind: artifactELF},
		{name: PreflightRunnerName, expectedSHA256: plan.PreflightRunnerSHA256, maxBytes: maxPreflightBytes, mode: 0o500, device: originalIdentity.dev, kind: artifactBash},
		{name: MigrationRunnerName, expectedSHA256: plan.MigrationRunnerSHA256, maxBytes: maxMigrationRunnerBytes, mode: 0o500, device: originalIdentity.dev, kind: artifactELF},
		{name: GooseBinaryName, expectedSHA256: plan.GooseBinarySHA256, maxBytes: maxGooseBytes, mode: 0o500, device: originalIdentity.dev, kind: artifactELF},
		{name: GlobalsDumpName, expectedSHA256: plan.GlobalsDumpSHA256, maxBytes: maxGlobalsDumpBytes, mode: 0o400, device: originalIdentity.dev, kind: artifactData},
		{name: DatabaseDumpName, expectedSHA256: plan.DatabaseDumpSHA256, maxBytes: maxDatabaseDumpBytes, mode: 0o400, device: originalIdentity.dev, kind: artifactPGDump},
	}
	for _, spec := range specs {
		artifact, openErr := openRetainedArtifactAt(ctx, duplicate, spec, plan.Architecture)
		if openErr != nil {
			return nil, fmt.Errorf("CA42 retain %s: %w", spec.name, openErr)
		}
		if spec.expectedChainSHA256 != "" {
			artifact.absolutePath = path.Join(IncomingRootPath, attemptID, spec.name)
			artifact.expectedChainHex = spec.expectedChainSHA256
			if chainErr := validateAbsoluteArtifactChain(ctx, artifact); chainErr != nil {
				artifact.file.Close()
				return nil, chainErr
			}
		}
		if registerErr := set.register(artifact); registerErr != nil {
			artifact.file.Close()
			return nil, registerErr
		}
	}
	if err := set.verifyAttestation(now); err != nil {
		return nil, err
	}

	migrationManifest, err := openRetainedArtifactAt(ctx, duplicate, artifactSpec{
		name: MigrationManifestName, expectedSHA256: plan.MigrationSetSHA256,
		maxBytes: migrationManifestMax, mode: 0o400, device: originalIdentity.dev, kind: artifactData,
	}, plan.Architecture)
	if err != nil {
		return nil, err
	}
	if err := set.register(migrationManifest); err != nil {
		migrationManifest.file.Close()
		return nil, err
	}
	manifestBytes, err := readRetainedArtifactBytes(migrationManifest)
	if err != nil {
		return nil, err
	}
	if plan.ClientAuth00042SHA256 != ca42manifest.FrozenMigrationSHA256 {
		return nil, errors.New("CA42 frozen migration hash mismatch")
	}
	entries, err := parseMigrationInventory(manifestBytes, plan.MigrationSetSHA256, plan.ClientAuth00042SHA256)
	if err != nil {
		return nil, err
	}
	migrationDirectory, migrationIdentity, err := openFixedArtifactDirectoryAt(duplicate, MigrationsDirectoryName, originalIdentity.dev)
	if err != nil {
		return nil, err
	}
	set.migrationDirectory, set.migrationIdentity = migrationDirectory, migrationIdentity
	if err := requireExactDirectoryEntries(migrationDirectory, entries); err != nil {
		return nil, err
	}
	for index, entry := range entries {
		artifact, openErr := openRetainedArtifactAt(ctx, migrationDirectory, artifactSpec{
			name: entry.name, expectedSHA256: hex.EncodeToString(entry.digest[:]), maxBytes: maxMigrationSQLBytes,
			mode: 0o400, device: migrationIdentity.dev, kind: artifactData,
		}, plan.Architecture)
		if openErr != nil {
			return nil, fmt.Errorf("CA42 retain migration %s: %w", entry.name, openErr)
		}
		artifact.relativeName = MigrationsDirectoryName + "/" + entry.name
		if registerErr := set.register(artifact); registerErr != nil {
			artifact.file.Close()
			return nil, registerErr
		}
		set.migrations = append(set.migrations, artifact)
		if index == migrationInventoryCount-1 {
			set.clientAuth00042 = artifact
		}
	}
	if set.clientAuth00042 == nil || set.clientAuth00042 != set.migrations[migrationInventoryCount-1] {
		return nil, errors.New("CA42 migration 00042 retained descriptor alias missing")
	}
	if err := set.revalidateLocked(ctx); err != nil {
		return nil, err
	}
	failed = false
	return set, nil
}

func (s *RetainedArtifactSet) register(artifact *retainedArtifact) error {
	if artifact == nil || artifact.file == nil || artifact.relativeName == "" {
		return errors.New("CA42 retained artifact invalid")
	}
	if _, exists := s.byName[artifact.relativeName]; exists {
		return errors.New("CA42 retained artifact name duplicate")
	}
	s.byName[artifact.relativeName] = artifact
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func openRetainedArtifactAt(ctx context.Context, parent *os.File, spec artifactSpec, architecture string) (*retainedArtifact, error) {
	if parent == nil || spec.name == "" || strings.ContainsAny(spec.name, "/\\\x00") || spec.name == "." || spec.name == ".." ||
		spec.maxBytes <= 0 || (spec.mode != 0o400 && spec.mode != 0o500) || spec.device == 0 ||
		!artifactSHA256Pattern.MatchString(spec.expectedSHA256) {
		return nil, errors.New("CA42 retained artifact request invalid")
	}
	fd, err := unix.Openat2(int(parent.Fd()), spec.name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), spec.name)
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("CA42 retained artifact descriptor conversion failed")
	}
	artifact, err := inspectRetainedArtifact(ctx, parent, file, spec.name, spec.maxBytes, spec.mode, spec.expectedSHA256, spec.device)
	if err != nil {
		file.Close()
		return nil, err
	}
	artifact.kind, artifact.architecture, artifact.owned = spec.kind, architecture, true
	if err := validateArtifactSemantics(artifact); err != nil {
		file.Close()
		return nil, err
	}
	return artifact, nil
}

func inspectExistingRetainedArtifact(ctx context.Context, parent, file *os.File, name string, maxBytes int64, mode uint32, expectedSHA string, device uint64) (*retainedArtifact, error) {
	artifact, err := inspectRetainedArtifact(ctx, parent, file, name, maxBytes, mode, expectedSHA, device)
	if err != nil {
		return nil, err
	}
	artifact.owned = false
	return artifact, nil
}

func inspectRetainedArtifact(ctx context.Context, parent, file *os.File, name string, maxBytes int64, mode uint32, expectedSHA string, device uint64) (*retainedArtifact, error) {
	if ctx == nil || parent == nil || file == nil || int(file.Fd()) < 3 || maxBytes <= 0 ||
		!artifactSHA256Pattern.MatchString(expectedSHA) {
		return nil, errors.New("CA42 retained artifact inspection invalid")
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, errors.New("CA42 retained artifact descriptor must be read-only")
	}
	before, err := retainedStat(int(file.Fd()))
	if err != nil || before.mode&unix.S_IFMT != unix.S_IFREG || before.uid != 0 || before.nlink != 1 ||
		before.mode&0o7777 != mode || before.size <= 0 || before.size > maxBytes || before.dev != device {
		return nil, errors.New("CA42 retained artifact ownership, mode, link, device, or size denied")
	}
	digest, err := hashStableRetainedArtifact(ctx, file, before, maxBytes)
	if err != nil {
		return nil, err
	}
	expected, _ := hex.DecodeString(expectedSHA)
	if subtle.ConstantTimeCompare(digest[:], expected) != 1 {
		return nil, errors.New("CA42 retained artifact SHA256 mismatch")
	}
	return &retainedArtifact{
		file: file, parent: parent, relativeName: name, identity: before, digest: digest,
		maxBytes: maxBytes, exactMode: mode, expectedDevice: device,
	}, nil
}

func hashStableRetainedArtifact(ctx context.Context, file *os.File, expected retainedIdentity, maxBytes int64) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if ctx == nil || file == nil || expected.size <= 0 || expected.size > maxBytes {
		return zero, errors.New("CA42 retained artifact hash request invalid")
	}
	hasher := sha256.New()
	if err := hashReaderAt(ctx, hasher, file, expected.size); err != nil {
		return zero, err
	}
	after, err := retainedStat(int(file.Fd()))
	if err != nil || after != expected {
		return zero, errors.New("CA42 retained artifact changed during hash")
	}
	copy(zero[:], hasher.Sum(nil))
	return zero, nil
}

func readRetainedArtifactBytes(artifact *retainedArtifact) ([]byte, error) {
	if artifact == nil || artifact.file == nil || artifact.identity.size <= 0 || artifact.identity.size > artifact.maxBytes {
		return nil, errors.New("CA42 retained artifact byte read invalid")
	}
	before, err := retainedStat(int(artifact.file.Fd()))
	if err != nil || before != artifact.identity {
		return nil, errors.New("CA42 retained artifact changed before byte read")
	}
	data := make([]byte, artifact.identity.size)
	if _, err := artifact.file.ReadAt(data, 0); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	after, err := retainedStat(int(artifact.file.Fd()))
	if err != nil || after != artifact.identity || subtle.ConstantTimeCompare(digest[:], artifact.digest[:]) != 1 {
		return nil, errors.New("CA42 retained artifact changed during byte read")
	}
	return data, nil
}

func validateArtifactSemantics(artifact *retainedArtifact) error {
	switch artifact.kind {
	case artifactData:
		return nil
	case artifactBash:
		header := make([]byte, len("#!/usr/bin/env bash\n"))
		if _, err := artifact.file.ReadAt(header, 0); err != nil || string(header) != "#!/usr/bin/env bash\n" {
			return errors.New("CA42 retained Bash script header invalid")
		}
		return nil
	case artifactPGDump:
		header := make([]byte, len("PGDMP"))
		if _, err := artifact.file.ReadAt(header, 0); err != nil || string(header) != "PGDMP" {
			return errors.New("CA42 retained database dump header invalid")
		}
		return nil
	case artifactEd25519PublicKey:
		data, err := readRetainedArtifactBytes(artifact)
		if err != nil {
			return err
		}
		_, err = parseCanonicalEd25519PublicKey(data)
		return err
	case artifactELF:
		parsed, err := elf.NewFile(artifact.file)
		if err != nil {
			return errors.New("CA42 retained executable ELF invalid")
		}
		defer parsed.Close()
		expectedMachine := elf.EM_X86_64
		if artifact.architecture == "arm64" {
			expectedMachine = elf.EM_AARCH64
		} else if artifact.architecture != "amd64" {
			return errors.New("CA42 retained executable architecture unsupported")
		}
		if artifact.architecture != runtime.GOARCH || parsed.Class != elf.ELFCLASS64 || parsed.Data != elf.ELFDATA2LSB ||
			(parsed.Type != elf.ET_EXEC && parsed.Type != elf.ET_DYN) || parsed.Version != elf.EV_CURRENT || parsed.Machine != expectedMachine {
			return errors.New("CA42 retained executable architecture mismatch")
		}
		return nil
	default:
		return errors.New("CA42 retained artifact kind invalid")
	}
}

func openFixedArtifactDirectoryAt(parent *os.File, name string, expectedDevice uint64) (*os.File, retainedIdentity, error) {
	if parent == nil || name != MigrationsDirectoryName || expectedDevice == 0 {
		return nil, retainedIdentity{}, errors.New("CA42 fixed artifact directory request invalid")
	}
	fd, err := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, retainedIdentity{}, err
	}
	identity, err := retainedStat(fd)
	if err != nil || identity.mode&unix.S_IFMT != unix.S_IFDIR || identity.uid != 0 || identity.nlink < 1 ||
		identity.mode&0o7777 != 0o700 || identity.dev != expectedDevice {
		unix.Close(fd)
		return nil, retainedIdentity{}, errors.New("CA42 fixed artifact directory identity denied")
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, retainedIdentity{}, errors.New("CA42 fixed artifact directory descriptor conversion failed")
	}
	return file, identity, nil
}

func requireExactDirectoryEntries(directory *os.File, expected []migrationInventoryEntry) error {
	if directory == nil || len(expected) != migrationInventoryCount {
		return errors.New("CA42 migration directory inventory invalid")
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if _, seekErr := directory.Seek(0, io.SeekStart); err == nil && seekErr != nil {
		err = seekErr
	}
	if err != nil || len(entries) != len(expected) {
		return errors.New("CA42 migration directory entry count mismatch")
	}
	actual := make([]string, len(entries))
	for index, entry := range entries {
		actual[index] = entry.Name()
	}
	sort.Strings(actual)
	expectedNames := make([]string, len(expected))
	for index, entry := range expected {
		expectedNames[index] = entry.name
	}
	if !equalStringSlices(actual, expectedNames) {
		return errors.New("CA42 migration directory entries mismatch")
	}
	return nil
}

func equalStringSlices(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func validateAbsoluteArtifactChain(ctx context.Context, artifact *retainedArtifact) error {
	if artifact == nil || artifact.file == nil || artifact.absolutePath == "" || artifact.expectedChainHex == "" ||
		!strings.HasPrefix(artifact.absolutePath, "/") || path.Clean(artifact.absolutePath) != artifact.absolutePath ||
		strings.Contains(artifact.absolutePath, "//") || !artifactSHA256Pattern.MatchString(artifact.expectedChainHex) {
		return errors.New("CA42 artifact path-chain request invalid")
	}
	actual, err := absoluteArtifactChainDigest(ctx, artifact.file, artifact.identity, artifact.absolutePath)
	if err != nil {
		return err
	}
	expected, _ := hex.DecodeString(artifact.expectedChainHex)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return errors.New("CA42 artifact path-chain SHA256 mismatch")
	}
	return nil
}

func absoluteArtifactChainDigest(ctx context.Context, file *os.File, identity retainedIdentity, absolutePath string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if ctx == nil || file == nil || absolutePath == "" || !strings.HasPrefix(absolutePath, "/") ||
		path.Clean(absolutePath) != absolutePath || strings.Contains(absolutePath, "//") {
		return zero, errors.New("CA42 artifact path-chain request invalid")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if retained, err := retainedStat(int(file.Fd())); err != nil || retained != identity {
		return zero, errors.New("CA42 artifact path-chain retained target changed")
	}
	components := strings.Split(strings.TrimPrefix(absolutePath, "/"), "/")
	currentFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return zero, err
	}
	hasher := sha256.New()
	defer func() { _ = unix.Close(currentFD) }()
	if err := appendPathChainRecord(hasher, 0, "directory", currentFD, false); err != nil {
		return zero, err
	}
	for index, component := range components {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if component == "" || component == "." || component == ".." {
			return zero, errors.New("CA42 artifact path component invalid")
		}
		last := index == len(components)-1
		flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		kind := "directory"
		if last {
			flags = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW
			kind = "artifact"
		}
		nextFD, openErr := unix.Openat2(currentFD, component, &unix.OpenHow{
			Flags: uint64(flags), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if openErr != nil {
			return zero, openErr
		}
		unix.Close(currentFD)
		currentFD = nextFD
		if err := appendPathChainRecord(hasher, index+1, kind, currentFD, last); err != nil {
			return zero, err
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
	}
	chainTarget, err := retainedStat(currentFD)
	if err != nil || chainTarget != identity {
		return zero, errors.New("CA42 artifact path-chain target binding mismatch")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func appendPathChainRecord(writer io.Writer, index int, kind string, fd int, artifact bool) error {
	identity, err := retainedStat(fd)
	if err != nil {
		return err
	}
	if artifact {
		if identity.mode&unix.S_IFMT != unix.S_IFREG || identity.uid != 0 || identity.gid != 0 || identity.nlink != 1 || identity.mode&0o022 != 0 {
			return errors.New("CA42 artifact path-chain target denied")
		}
	} else if identity.mode&unix.S_IFMT != unix.S_IFDIR || identity.uid != 0 || identity.gid != 0 || identity.nlink < 1 || identity.mode&0o022 != 0 {
		return errors.New("CA42 artifact path-chain ancestor denied")
	}
	_, err = fmt.Fprintf(writer, "%d|%s|%d|%d|%d|%04o|%d\n", index, kind, identity.dev, identity.ino, identity.uid, identity.mode&0o7777, identity.nlink)
	return err
}

func (s *RetainedArtifactSet) validateAttemptBinding() error {
	incoming, err := openFixedTrustedDirectory(IncomingRootPath)
	if err != nil {
		return err
	}
	defer incoming.Close()
	reopened, err := openTrustedChildDirectory(int(incoming.Fd()), s.attemptID)
	if err != nil {
		return err
	}
	defer reopened.Close()
	identity, err := retainedStat(int(reopened.Fd()))
	if err != nil || identity != s.attemptIdentity {
		return errors.New("CA42 retained attempt root basename binding changed")
	}
	return nil
}

func (s *RetainedArtifactSet) Revalidate(ctx context.Context) error {
	if s == nil {
		return errors.New("CA42 retained artifact set closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revalidateLocked(ctx)
}

func (s *RetainedArtifactSet) revalidateLocked(ctx context.Context) error {
	if s.closed || s.attemptRoot == nil || s.migrationDirectory == nil || ctx == nil {
		return errors.New("CA42 retained artifact set closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateAttemptBinding(); err != nil {
		return err
	}
	if identity, err := retainedStat(int(s.attemptRoot.Fd())); err != nil || identity != s.attemptIdentity {
		return errors.New("CA42 retained attempt root changed")
	}
	if identity, err := retainedStat(int(s.migrationDirectory.Fd())); err != nil || identity != s.migrationIdentity {
		return errors.New("CA42 retained migration directory changed")
	}
	reboundMigration, reboundIdentity, err := openFixedArtifactDirectoryAt(s.attemptRoot, MigrationsDirectoryName, s.attemptIdentity.dev)
	if err != nil {
		return err
	}
	reboundMigration.Close()
	if reboundIdentity != s.migrationIdentity {
		return errors.New("CA42 migration directory basename binding changed")
	}
	entries := make([]migrationInventoryEntry, len(s.migrations))
	for index, artifact := range s.migrations {
		entries[index] = migrationInventoryEntry{name: ca42MigrationNames[index], digest: artifact.digest}
	}
	if err := requireExactDirectoryEntries(s.migrationDirectory, entries); err != nil {
		return err
	}
	if identity, err := retainedStat(int(s.migrationDirectory.Fd())); err != nil || identity != s.migrationIdentity {
		return errors.New("CA42 retained migration directory changed during enumeration")
	}
	for _, artifact := range s.artifacts {
		if err := revalidateArtifact(ctx, artifact); err != nil {
			return fmt.Errorf("CA42 retained artifact %s changed: %w", artifact.relativeName, err)
		}
		if artifact.expectedChainHex != "" {
			if err := validateAbsoluteArtifactChain(ctx, artifact); err != nil {
				return err
			}
		}
	}
	if s.clientAuth00042 == nil || s.clientAuth00042 != s.migrations[migrationInventoryCount-1] {
		return errors.New("CA42 migration 00042 retained descriptor alias changed")
	}
	if err := s.verifyAttestation(s.attestationWindow.notBefore); err != nil {
		return err
	}
	for _, artifact := range s.artifacts {
		if artifact.expectedChainHex != "" {
			if err := validateAbsoluteArtifactChain(ctx, artifact); err != nil {
				return err
			}
		}
	}
	if err := s.validateAttemptBinding(); err != nil {
		return err
	}
	if identity, err := retainedStat(int(s.attemptRoot.Fd())); err != nil || identity != s.attemptIdentity {
		return errors.New("CA42 retained attempt root changed after revalidation")
	}
	if identity, err := retainedStat(int(s.migrationDirectory.Fd())); err != nil || identity != s.migrationIdentity {
		return errors.New("CA42 retained migration directory changed after revalidation")
	}
	reboundMigration, reboundIdentity, err = openFixedArtifactDirectoryAt(s.attemptRoot, MigrationsDirectoryName, s.attemptIdentity.dev)
	if err != nil {
		return err
	}
	reboundMigration.Close()
	if reboundIdentity != s.migrationIdentity {
		return errors.New("CA42 migration directory final basename binding changed")
	}
	return nil
}

func revalidateArtifact(ctx context.Context, artifact *retainedArtifact) error {
	if artifact == nil || artifact.file == nil || artifact.parent == nil {
		return errors.New("retained artifact missing")
	}
	name := artifact.relativeName
	if strings.Contains(name, "/") {
		name = path.Base(name)
	}
	reboundFD, err := unix.Openat2(int(artifact.parent.Fd()), name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return err
	}
	reboundIdentity, statErr := retainedStat(reboundFD)
	unix.Close(reboundFD)
	if statErr != nil || reboundIdentity != artifact.identity {
		return errors.New("basename binding mismatch")
	}
	currentIdentity, err := retainedStat(int(artifact.file.Fd()))
	if err != nil || currentIdentity != artifact.identity {
		return errors.New("descriptor identity mismatch")
	}
	digest, err := hashStableRetainedArtifact(ctx, artifact.file, artifact.identity, artifact.maxBytes)
	if err != nil || subtle.ConstantTimeCompare(digest[:], artifact.digest[:]) != 1 {
		return errors.New("content digest mismatch")
	}
	if err := validateArtifactSemantics(artifact); err != nil {
		return err
	}
	reboundFD, err = unix.Openat2(int(artifact.parent.Fd()), name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return err
	}
	reboundIdentity, statErr = retainedStat(reboundFD)
	unix.Close(reboundFD)
	if statErr != nil || reboundIdentity != artifact.identity {
		return errors.New("final basename binding mismatch")
	}
	return nil
}

func (s *RetainedArtifactSet) verifyAttestation(now time.Time) error {
	attestation := s.byName[AttestationName]
	expected := s.byName[AttestationExpectedName]
	publicKey := s.byName[AttestationPublicKeyName]
	if attestation == nil || expected == nil || publicKey == nil || s.externalManifest == nil {
		return errors.New("CA42 retained attestation artifacts missing")
	}
	attestationBytes, err := readRetainedArtifactBytes(attestation)
	if err != nil {
		return err
	}
	expectedBytes, err := readRetainedArtifactBytes(expected)
	if err != nil {
		return err
	}
	publicKeyBytes, err := readRetainedArtifactBytes(publicKey)
	if err != nil {
		return err
	}
	externalBytes, err := readRetainedArtifactBytes(s.externalManifest)
	if err != nil {
		return err
	}
	manifestReceipt, err := ca42manifest.Verify(externalBytes)
	if err != nil {
		return err
	}
	window, err := verifyRetainedAttestationV2(attestationBytes, expectedBytes, publicKeyBytes, s.plan, s.capsule, manifestReceipt, now)
	if err != nil {
		return err
	}
	if !s.attestationWindow.notBefore.IsZero() && window != s.attestationWindow {
		return errors.New("CA42 retained attestation validity changed")
	}
	s.manifestReceipt, s.attestationWindow = manifestReceipt, window
	return nil
}

func (s *RetainedArtifactSet) ValidateAt(now time.Time) error {
	if s == nil {
		return errors.New("CA42 retained artifact set closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("CA42 retained artifact set closed")
	}
	return s.attestationWindow.validateAt(now)
}

func (s *RetainedArtifactSet) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var result error
	for index := len(s.artifacts) - 1; index >= 0; index-- {
		artifact := s.artifacts[index]
		if artifact != nil && artifact.owned && artifact.file != nil {
			result = errors.Join(result, artifact.file.Close())
			artifact.file = nil
		}
	}
	if s.migrationDirectory != nil {
		result = errors.Join(result, s.migrationDirectory.Close())
		s.migrationDirectory = nil
	}
	if s.attemptRoot != nil {
		result = errors.Join(result, s.attemptRoot.Close())
		s.attemptRoot = nil
	}
	s.byName = nil
	s.migrations = nil
	s.clientAuth00042 = nil
	return result
}
