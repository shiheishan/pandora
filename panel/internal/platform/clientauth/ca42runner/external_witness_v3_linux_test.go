//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

func TestV3ExternalWitnessOneShotAndDestroy(t *testing.T) {
	raw := []byte("verified external evidence")
	digest := sha256.Sum256(raw)
	witness := &v3ExternalWitness{
		raw: raw, digest: digest,
		receipt: testV3WitnessReceipt(digest),
	}
	var escaped []byte
	if err := witness.use(context.Background(), func(_ context.Context, value []byte, receipt ca42manifest.Receipt, actual [sha256.Size]byte) error {
		escaped = value
		if string(value) != "verified external evidence" || receipt.Decision != "STRUCTURALLY_VALID" || actual != digest {
			t.Fatal("witness operation received changed evidence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := witness.use(context.Background(), func(context.Context, []byte, ca42manifest.Receipt, [sha256.Size]byte) error { return nil }); !errors.Is(err, errV3ExternalWitnessUnavailable) {
		t.Fatalf("second witness consumption accepted: %v", err)
	}
	witness.destroy()
	witness.destroy()
	for _, value := range escaped {
		if value != 0 {
			t.Fatal("destroy did not zero escaped witness bytes")
		}
	}
	if witness.raw != nil || witness.receipt != (ca42manifest.Receipt{}) || witness.digest != ([sha256.Size]byte{}) || !witness.destroyed {
		t.Fatal("destroy did not revoke witness state")
	}
}

func TestV3ExternalWitnessPanicAndGoexitRemainDestroyable(t *testing.T) {
	for _, test := range []struct {
		name string
		exit func()
	}{
		{"panic", func() { panic("witness panic") }},
		{"goexit", runtime.Goexit},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte("temporary evidence")
			digest := sha256.Sum256(raw)
			witness := &v3ExternalWitness{raw: raw, digest: digest, receipt: testV3WitnessReceipt(digest)}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() { _ = recover() }()
				_ = witness.use(context.Background(), func(context.Context, []byte, ca42manifest.Receipt, [sha256.Size]byte) error {
					test.exit()
					return nil
				})
			}()
			<-done
			for _, value := range raw {
				if value != 0 {
					t.Fatal("abnormal exit left witness bytes live")
				}
			}
			if !witness.consumed || witness.raw != nil {
				t.Fatal("abnormal exit did not revoke witness")
			}
			witness.destroy()
		})
	}
}

func testV3WitnessReceipt(digest [sha256.Size]byte) ca42manifest.Receipt {
	return ca42manifest.Receipt{
		ReceiptFormat: "client-auth-00042-external-manifest-structural-receipt-v1",
		Decision:      "STRUCTURALLY_VALID", Authorization: "NONE",
		ExternalManifestSHA256:    hex.EncodeToString(digest[:]),
		ExactObjectManifestSHA256: "non-empty", ContractSHA256: ca42manifest.ContractSHA256,
	}
}
