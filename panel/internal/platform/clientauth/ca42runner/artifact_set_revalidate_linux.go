//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 artifact_set_linux.go 的 RetainedArtifactSet 与 retainedArtifact，依赖 artifact_set_chain_linux.go 的路径链校验，依赖同包的证明校验
// [OUTPUT]: 对外提供 RetainedArtifactSet 的 Revalidate、ValidateAt、Close；包内 validateAttemptBinding、revalidateLocked、revalidateArtifact、verifyAttestation
// [POS]: ca42runner 制品集的复核、证明与关闭：从 artifact_set_linux.go 拆出。复核在锁内逐个制品比对身份与路径链，ValidateAt 按给定时刻校验证明；Close 只关本集合新开的描述符，与 VerificationSession 共享的别名不关
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca42runner

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

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
