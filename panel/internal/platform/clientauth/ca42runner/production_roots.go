package ca42runner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

const productionRootFingerprintDomain = "PANDORA\x00CA42-PRODUCTION-ROOT-KEYSET\x00V1\x00"

var productionRootToken = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

var (
	errProductionRootLeaseInvalid = errors.New("CA42 production authority roots invalid")
	errProductionRootLeaseClosed  = errors.New("CA42 production authority roots unavailable")
)

// productionRootLease is the package-private production authority capability.
// It deliberately exposes neither root IDs nor public-key bytes. Shallow copies
// share one close state and therefore cannot revive or double-close the lease.
type productionRootLease struct {
	state *productionRootLeaseState
}

type productionRootLeaseState struct {
	mu          sync.Mutex
	roots       ca42authority.RootKeyset
	fingerprint [sha256.Size]byte
	closed      bool
}

type compiledRootProvider func() (ca42authority.RootKeyset, error)

func openProductionRootLease(ctx context.Context) (*productionRootLease, error) {
	return openProductionRootLeaseWith(ctx, CompiledRootKeyset)
}

func openProductionRootLeaseWith(ctx context.Context, provider compiledRootProvider) (*productionRootLease, error) {
	if ctx == nil || provider == nil {
		return nil, errProductionRootLeaseInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	roots, err := provider()
	if err != nil {
		return nil, errors.Join(errProductionRootLeaseInvalid, err)
	}
	trusted, fingerprint, err := cloneAndFingerprintProductionRoots(roots)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		zeroRootKeyset(&trusted)
		return nil, err
	}
	return &productionRootLease{state: &productionRootLeaseState{
		roots: trusted, fingerprint: fingerprint,
	}}, nil
}

// withRootKeyset passes a fresh private copy only to package-internal parsing
// code. The stored keyset is fingerprinted before and after the operation, so
// corruption or mutation poisons the capability rather than being published.
func (lease *productionRootLease) withRootKeyset(ctx context.Context, operation func(ca42authority.RootKeyset) error) error {
	if lease == nil || lease.state == nil || ctx == nil || operation == nil {
		return errProductionRootLeaseInvalid
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return errProductionRootLeaseClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	trusted, fingerprint, err := cloneAndFingerprintProductionRoots(state.roots)
	if err != nil || fingerprint != state.fingerprint {
		state.closed = true
		zeroRootKeyset(&state.roots)
		state.fingerprint = [sha256.Size]byte{}
		zeroRootKeyset(&trusted)
		return errors.Join(errProductionRootLeaseClosed, err)
	}
	defer zeroRootKeyset(&trusted)
	operationErr := operation(trusted)
	currentRoots, current, fingerprintErr := cloneAndFingerprintProductionRoots(state.roots)
	zeroRootKeyset(&currentRoots)
	if fingerprintErr != nil || current != state.fingerprint {
		state.closed = true
		zeroRootKeyset(&state.roots)
		state.fingerprint = [sha256.Size]byte{}
		return errors.Join(errProductionRootLeaseClosed, fingerprintErr, operationErr)
	}
	if operationErr != nil {
		return operationErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// verifyAuthorityAt is the only production use of the transient scalar
// RootKeyset. No caller can supply or receive roots through this boundary.
func (lease *productionRootLease) verifyAuthorityAt(ctx context.Context, authorityBytes []byte, architecture string,
	hostIdentity [sha256.Size]byte, now time.Time) (ca42authority.Descriptor, error) {
	var descriptor ca42authority.Descriptor
	err := lease.withRootKeyset(ctx, func(roots ca42authority.RootKeyset) error {
		verified, err := ca42authority.ParseAndVerify(authorityBytes, roots, architecture, hostIdentity, now)
		if err != nil {
			return err
		}
		descriptor = verified
		return nil
	})
	if err != nil {
		return ca42authority.Descriptor{}, err
	}
	return descriptor, nil
}

func (lease *productionRootLease) Close() error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	zeroRootKeyset(&state.roots)
	state.fingerprint = [sha256.Size]byte{}
	return nil
}

func cloneAndFingerprintProductionRoots(roots ca42authority.RootKeyset) (ca42authority.RootKeyset, [sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if !productionRootToken.MatchString(roots.ID) || roots.Quorum != ca42authority.RequiredQuorum ||
		len(roots.Keys) != ca42authority.RequiredRootCount {
		return ca42authority.RootKeyset{}, empty, errProductionRootLeaseInvalid
	}
	trusted := ca42authority.RootKeyset{ID: roots.ID, Quorum: roots.Quorum, Keys: make([]ca42authority.RootKey, len(roots.Keys))}
	seen := make(map[[sha256.Size]byte]struct{}, len(roots.Keys))
	previous := ""
	for index, key := range roots.Keys {
		if !productionRootToken.MatchString(key.ID) || len(key.PublicKey) != ed25519.PublicKeySize ||
			(previous != "" && key.ID <= previous) {
			zeroRootKeyset(&trusted)
			return ca42authority.RootKeyset{}, empty, errProductionRootLeaseInvalid
		}
		digest := sha256.Sum256(key.PublicKey)
		if _, duplicate := seen[digest]; duplicate {
			zeroRootKeyset(&trusted)
			return ca42authority.RootKeyset{}, empty, errProductionRootLeaseInvalid
		}
		seen[digest] = struct{}{}
		trusted.Keys[index] = ca42authority.RootKey{ID: key.ID, PublicKey: append(ed25519.PublicKey(nil), key.PublicKey...)}
		previous = key.ID
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(productionRootFingerprintDomain))
	writeRootFingerprintField(hash, []byte(trusted.ID))
	var scalar [8]byte
	binary.BigEndian.PutUint64(scalar[:], uint64(trusted.Quorum))
	_, _ = hash.Write(scalar[:])
	binary.BigEndian.PutUint64(scalar[:], uint64(len(trusted.Keys)))
	_, _ = hash.Write(scalar[:])
	for _, key := range trusted.Keys {
		writeRootFingerprintField(hash, []byte(key.ID))
		writeRootFingerprintField(hash, key.PublicKey)
	}
	copy(empty[:], hash.Sum(nil))
	return trusted, empty, nil
}

type rootFingerprintWriter interface {
	Write([]byte) (int, error)
}

func writeRootFingerprintField(hash rootFingerprintWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write(value)
}

func zeroRootKeyset(roots *ca42authority.RootKeyset) {
	if roots == nil {
		return
	}
	for index := range roots.Keys {
		for byteIndex := range roots.Keys[index].PublicKey {
			roots.Keys[index].PublicKey[byteIndex] = 0
		}
		roots.Keys[index] = ca42authority.RootKey{}
	}
	roots.ID = ""
	roots.Quorum = 0
	roots.Keys = nil
}
