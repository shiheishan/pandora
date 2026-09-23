//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
)

// InspectLayoutSwitchedFor cross-binds the retained sequence-6 Journal session
// to an opaque, currently valid ArtifactSet journal capability. The exact
// signed head and manifest transitively bind PREPARED.controller_sha256 and all
// six transitions, so no detached caller-provided controller hash is trusted.
func (s *V3Session) InspectLayoutSwitchedFor(binding ca42artifactsv2.JournalBinding, now time.Time) (V3Snapshot, error) {
	if now.IsZero() {
		return V3Snapshot{}, errors.New("release journal v3 artifact boundary trusted time invalid")
	}
	before, err := binding.ValuesAt(now)
	if err != nil {
		return V3Snapshot{}, err
	}
	boundary, err := s.InspectLayoutSwitched()
	if err != nil {
		return V3Snapshot{}, err
	}
	if err := bindV3LayoutSwitchedValues(boundary, before); err != nil {
		return V3Snapshot{}, err
	}
	after, err := binding.ValuesAt(now)
	if err != nil || after != before {
		return V3Snapshot{}, errors.New("release journal v3 artifact binding changed during inspection")
	}
	return boundary, nil
}

func bindV3LayoutSwitchedValues(boundary V3Snapshot, values ca42artifactsv2.JournalBindingValues) error {
	if verified, err := boundary.VerifiedCopy(); err != nil || verified.State() != V3LayoutSwitched || verified.Sequence() != 6 {
		return errors.New("release journal v3 artifact layout boundary invalid")
	}
	if values.AttemptID == "" || values.ReleaseContractCoreSHA256 == "" || values.ReleaseJournalHeadSHA256 == "" ||
		values.ReleaseJournalSnapshotSHA256 == "" || values.ArtifactSetBindingSHA256 == "" ||
		boundary.AttemptID() != values.AttemptID || boundary.ReleaseContractCoreSHA256() != values.ReleaseContractCoreSHA256 ||
		boundary.HeadSHA256() != values.ReleaseJournalHeadSHA256 || boundary.ManifestSHA256() != values.ReleaseJournalSnapshotSHA256 {
		return errors.New("release journal v3 artifact boundary binding mismatch")
	}
	return nil
}
