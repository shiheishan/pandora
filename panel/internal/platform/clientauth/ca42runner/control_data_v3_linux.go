//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
)

type v3ControlDatum struct {
	bytes  []byte
	digest [sha256.Size]byte
}

type v3ControlData struct {
	release, plan, capsule, attestation, expected, storage v3ControlDatum
}

type v3ControlDataOperation func(context.Context, *v3ControlData) error

func (source *productionV3ControlBundle) withControlData(ctx context.Context, operation v3ControlDataOperation) error {
	if source == nil || source.bundle == nil || source.bundle.state == nil || ctx == nil || operation == nil {
		return errV3ControlBundleInvalid
	}
	state := source.bundle.state
	state.mu.Lock()
	production := state.productionOrigin && !state.closed && !state.poisoned
	state.mu.Unlock()
	if !production {
		return errV3ControlBundleUnavailable
	}
	return withV3ControlDataFromRetained(ctx, source.bundle, operation)
}

func withV3ControlDataFromRetained(ctx context.Context, bundle *retainedV3ControlBundle, operation v3ControlDataOperation) (result error) {
	if ctx == nil || bundle == nil || bundle.state == nil || operation == nil {
		return errV3ControlBundleInvalid
	}
	data := &v3ControlData{}
	owned := make([][]byte, 0, 6)
	completed := false
	defer func() {
		for _, buffer := range owned {
			zeroBytes(buffer)
		}
		zeroV3ControlData(data)
		if !completed {
			result = errors.Join(result, bundle.Close())
		}
	}()

	roles := [...]struct {
		role   string
		target *v3ControlDatum
	}{
		{ca42controlv3.ReleaseManifestRole, &data.release},
		{ca42controlv3.ExecutionPlanRole, &data.plan},
		{ca42controlv3.TrustCapsuleRole, &data.capsule},
		{ca42controlv3.AttestationRole, &data.attestation},
		{ca42controlv3.ExpectedRole, &data.expected},
		{ca42controlv3.ArtifactStorageDescriptorRole, &data.storage},
	}
	for _, candidate := range roles {
		err := bundle.withRoleReaderAt(ctx, candidate.role,
			func(_ context.Context, reader io.ReaderAt, size uint64, digest [sha256.Size]byte) error {
				if size == 0 || size > uint64(int(^uint(0)>>1)) {
					return errV3ControlBundleInvalid
				}
				buffer := make([]byte, int(size))
				if _, err := io.ReadFull(io.NewSectionReader(reader, 0, int64(size)), buffer); err != nil {
					zeroBytes(buffer)
					return errors.Join(errV3ControlBundleUnavailable, err)
				}
				actual := sha256.Sum256(buffer)
				if subtle.ConstantTimeCompare(actual[:], digest[:]) != 1 {
					zeroBytes(buffer)
					return errV3ControlBundleUnavailable
				}
				candidate.target.bytes, candidate.target.digest = buffer, digest
				return nil
			})
		if err != nil {
			return err
		}
		owned = append(owned, candidate.target.bytes)
	}
	if err := operation(ctx, data); err != nil {
		return err
	}
	if err := bundle.Revalidate(ctx); err != nil {
		return err
	}
	completed = true
	return nil
}

func zeroV3ControlData(data *v3ControlData) {
	if data == nil {
		return
	}
	for _, datum := range []*v3ControlDatum{&data.release, &data.plan, &data.capsule, &data.attestation, &data.expected, &data.storage} {
		zeroBytes(datum.bytes)
		datum.bytes = nil
		datum.digest = [sha256.Size]byte{}
	}
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
