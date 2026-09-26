//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖同包 files_linux.go 的 retainedIdentity 与可信目录原语、artifact_manifest.go 的迁移清单、artifact_stream.go 的流式哈希，依赖 golang.org/x/sys/unix
// [OUTPUT]: 对外提供 RetainedArtifactSet；包内提供 openRetainedArtifactSet、制品的打开、检查、稳定哈希与语义校验
// [POS]: ca42runner 制品集的打开与检查：RetainedArtifactSet 持有每个新打开的 CA42 制品描述符，信任胶囊与外部清单条目刻意复用 VerificationSession 已保留的描述符；生产 API 不暴露任何描述符。路径链在 artifact_set_chain_linux.go，复核与关闭在 artifact_set_revalidate_linux.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsule"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
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
