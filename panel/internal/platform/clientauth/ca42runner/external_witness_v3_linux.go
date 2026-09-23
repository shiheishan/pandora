//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"sync"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

var (
	errV3ExternalWitnessInvalid     = errors.New("CA42 V3 external witness invalid")
	errV3ExternalWitnessUnavailable = errors.New("CA42 V3 external witness unavailable")
)

// v3ExternalWitness is callback-scoped evidence captured exactly once from the
// retained production inventory. It owns the only caller-visible raw buffer and
// must be destroyed by its composer on every exit path.
type v3ExternalWitness struct {
	mu        sync.Mutex
	raw       []byte
	receipt   ca42manifest.Receipt
	digest    [sha256.Size]byte
	consumed  bool
	destroyed bool
}

type v3ExternalWitnessOperation func(context.Context, []byte, ca42manifest.Receipt, [sha256.Size]byte) error

func captureV3ExternalWitness(ctx context.Context, inventory *ca42storage.InventoryLease) (*v3ExternalWitness, error) {
	if ctx == nil || inventory == nil {
		return nil, errV3ExternalWitnessInvalid
	}
	var witness *v3ExternalWitness
	err := inventory.WithRoleReaderAt(ctx, "external_manifest", ca42manifest.MaxManifestBytes,
		func(operationContext context.Context, reader io.ReaderAt, size uint64) error {
			if size == 0 || size > ca42manifest.MaxManifestBytes || size > uint64(int(^uint(0)>>1)) {
				return errV3ExternalWitnessUnavailable
			}
			raw := make([]byte, int(size))
			owned := true
			defer func() {
				if owned {
					zeroBytes(raw)
				}
			}()
			if err := operationContext.Err(); err != nil {
				return err
			}
			if _, err := io.ReadFull(io.NewSectionReader(reader, 0, int64(size)), raw); err != nil {
				return errors.Join(errV3ExternalWitnessUnavailable, err)
			}
			if err := operationContext.Err(); err != nil {
				return err
			}
			digest := sha256.Sum256(raw)
			receipt, err := ca42manifest.Verify(raw)
			if err != nil {
				return errors.Join(errV3ExternalWitnessUnavailable, err)
			}
			if err := operationContext.Err(); err != nil {
				return err
			}
			receiptDigest, err := hex.DecodeString(receipt.ExternalManifestSHA256)
			if err != nil || len(receiptDigest) != sha256.Size || subtle.ConstantTimeCompare(receiptDigest, digest[:]) != 1 {
				return errV3ExternalWitnessUnavailable
			}
			witness = &v3ExternalWitness{raw: raw, receipt: receipt, digest: digest}
			owned = false
			return nil
		})
	if err != nil {
		if witness != nil {
			witness.destroy()
		}
		return nil, err
	}
	if witness == nil {
		return nil, errV3ExternalWitnessUnavailable
	}
	return witness, nil
}

func (witness *v3ExternalWitness) use(ctx context.Context, operation v3ExternalWitnessOperation) error {
	if witness == nil || ctx == nil || operation == nil {
		return errV3ExternalWitnessInvalid
	}
	witness.mu.Lock()
	if witness.destroyed || witness.consumed || len(witness.raw) == 0 || witness.digest == ([sha256.Size]byte{}) {
		witness.mu.Unlock()
		return errV3ExternalWitnessUnavailable
	}
	raw, receipt, digest := witness.raw, witness.receipt, witness.digest
	witness.consumed = true
	witness.raw = nil
	witness.receipt = ca42manifest.Receipt{}
	witness.digest = [sha256.Size]byte{}
	witness.mu.Unlock()
	defer zeroBytes(raw)
	if err := ctx.Err(); err != nil {
		return err
	}
	actual := sha256.Sum256(raw)
	if !validV3WitnessReceipt(receipt, digest) || subtle.ConstantTimeCompare(actual[:], digest[:]) != 1 {
		return errV3ExternalWitnessUnavailable
	}
	if err := operation(ctx, raw, receipt, digest); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	actual = sha256.Sum256(raw)
	if !validV3WitnessReceipt(receipt, digest) || subtle.ConstantTimeCompare(actual[:], digest[:]) != 1 {
		return errV3ExternalWitnessUnavailable
	}
	return nil
}

func validV3WitnessReceipt(receipt ca42manifest.Receipt, digest [sha256.Size]byte) bool {
	decoded, err := hex.DecodeString(receipt.ExternalManifestSHA256)
	return err == nil && len(decoded) == sha256.Size &&
		receipt.ReceiptFormat == "client-auth-00042-external-manifest-structural-receipt-v1" &&
		receipt.Decision == "STRUCTURALLY_VALID" && receipt.Authorization == "NONE" &&
		receipt.ContractSHA256 == ca42manifest.ContractSHA256 && receipt.ExactObjectManifestSHA256 != "" &&
		subtle.ConstantTimeCompare(decoded, digest[:]) == 1
}

func (witness *v3ExternalWitness) destroy() {
	if witness == nil {
		return
	}
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.destroyed {
		return
	}
	zeroBytes(witness.raw)
	witness.raw = nil
	witness.receipt = ca42manifest.Receipt{}
	witness.digest = [sha256.Size]byte{}
	witness.consumed = true
	witness.destroyed = true
}
