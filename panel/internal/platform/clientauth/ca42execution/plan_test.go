package ca42execution

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42release"
)

func TestParseAndBindRelease(t *testing.T) {
	values := validPlanValues()
	data, err := CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	plan, err := Parse(data, digest, "amd64", time.Unix(1700000100, 0))
	if err != nil {
		t.Fatal(err)
	}
	release := matchingRelease(plan)
	if err := BindRelease(plan, release); err != nil {
		t.Fatal(err)
	}
	if plan.SourceDatabaseOID != "16384" || plan.IsolatedDatabaseOID != "24576" ||
		plan.SourceSystemIdentifier == plan.IsolatedSystemIdentifier {
		t.Fatalf("parsed plan lost database identity: %#v", plan)
	}

	release.ExecutionPlanSHA256 = strings.Repeat("9", 64)
	if err := BindRelease(plan, release); err == nil {
		t.Fatal("accepted release that did not pin the execution plan")
	}
}

func TestParseRejectsCanonicalAndIdentityDrift(t *testing.T) {
	base := validPlanValues()
	baseData, err := CanonicalBytes(base)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest := sha256.Sum256(baseData)
	now := time.Unix(1700000100, 0)

	tests := []struct {
		name string
		data func() []byte
		sha  [sha256.Size]byte
		now  time.Time
	}{
		{name: "content changed without signed digest change", data: func() []byte {
			changed := append([]byte(nil), baseData...)
			changed[len(changed)-2] = '1'
			return changed
		}, sha: baseDigest, now: now},
		{name: "carriage return", data: func() []byte { return append([]byte(nil), append(baseData[:len(baseData)-1], '\r', '\n')...) }, sha: sha256.Sum256(append([]byte(nil), append(baseData[:len(baseData)-1], '\r', '\n')...)), now: now},
		{name: "missing final newline", data: func() []byte { return baseData[:len(baseData)-1] }, sha: sha256.Sum256(baseData[:len(baseData)-1]), now: now},
		{name: "expired", data: func() []byte { return baseData }, sha: baseDigest, now: time.Unix(1700000300, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.data(), test.sha, "amd64", test.now); err == nil {
				t.Fatal("accepted invalid execution plan")
			}
		})
	}

	mutations := []struct {
		name  string
		index int
		value string
	}{
		{name: "unknown architecture", index: idxArchitecture, value: "riscv64"},
		{name: "uppercase hash", index: idxTrustCapsuleSHA, value: strings.Repeat("A", 64)},
		{name: "zero hash", index: idxTrustCapsuleSHA, value: strings.Repeat("0", 64)},
		{name: "migration hash drift", index: idxClientAuth00042SHA, value: strings.Repeat("8", 64)},
		{name: "source equals isolated container", index: idxIsolatedContainerID, value: strings.Repeat("a", 64)},
		{name: "source equals isolated system", index: idxIsolatedSystemID, value: "1111111111111111111"},
		{name: "database drift", index: idxIsolatedDatabase, value: "other"},
		{name: "OID overflow", index: idxIsolatedDatabaseOID, value: "4294967296"},
		{name: "leading zero", index: idxSourceDatabaseOID, value: "016384"},
		{name: "wrong image binding", index: idxIsolatedImageID, value: "sha256:" + strings.Repeat("8", 64)},
		{name: "validity too long", index: idxNotAfter, value: "1700003601"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			values := append([]string(nil), base...)
			values[mutation.index] = mutation.value
			data, err := CanonicalBytes(values)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			if _, err := Parse(data, digest, "amd64", now); err == nil {
				t.Fatal("accepted semantically invalid execution plan")
			}
		})
	}
}

func TestBindReleaseRejectsMixAndMatch(t *testing.T) {
	data, err := CanonicalBytes(validPlanValues())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	plan, err := Parse(data, digest, "amd64", time.Unix(1700000100, 0))
	if err != nil {
		t.Fatal(err)
	}
	release := matchingRelease(plan)
	release.AttemptID = "attempt-other"
	if err := BindRelease(plan, release); err == nil {
		t.Fatal("accepted plan from another attempt")
	}
	release = matchingRelease(plan)
	release.DatabaseDumpSHA256 = strings.Repeat("8", 64)
	if err := BindRelease(plan, release); err == nil {
		t.Fatal("accepted plan with a different database dump")
	}
	release = matchingRelease(plan)
	release.NotAfter = release.NotAfter.Add(-time.Second)
	if err := BindRelease(plan, release); err == nil {
		t.Fatal("accepted plan with a different validity window")
	}
}

func validPlanValues() []string {
	return []string{
		Format, Status, Transition, "release-ca42-1", "run-ca42-1", "attempt-ca42-1", "amd64",
		strings.Repeat("1", 64), strings.Repeat("2", 64), "2049", RequiredExecutableMode,
		strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64), "2050", RequiredExecutableMode,
		strings.Repeat("6", 64), strings.Repeat("7", 64), strings.Repeat("e", 64), strings.Repeat("8", 64),
		strings.Repeat("9", 64), strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64),
		strings.Repeat("d", 64), ca42manifest.FrozenMigrationSHA256, strings.Repeat("f", 64), strings.Repeat("1", 64),
		strings.Repeat("c", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), LedgerNamespace,
		LedgerDirectorySHA256, strings.Repeat("a", 64), "1111111111111111111", "aegis", "16384", "10",
		"aegis_owner", "41", strings.Repeat("b", 64), "2222222222222222222", strings.Repeat("d", 64),
		"aegis", "24576", "sha256:" + strings.Repeat("c", 64), "pandoraisolatedpg18ABC123-1700000000",
		"1700000000", "1700000300",
	}
}

func matchingRelease(plan Plan) ca42release.Manifest {
	return ca42release.Manifest{
		Architecture: plan.Architecture, ReleaseID: plan.ReleaseID, ReleaseRunID: plan.ReleaseRunID,
		AttemptID: plan.AttemptID, MigrationRunnerSHA256: plan.MigrationRunnerSHA256,
		ManifestVerifierSHA256: plan.ManifestVerifierSHA256, PreflightRunnerSHA256: plan.PreflightRunnerSHA256,
		GooseBinarySHA256: plan.GooseBinarySHA256, MigrationSetSHA256: plan.MigrationSetSHA256,
		ClientAuth00042SHA256: plan.ClientAuth00042SHA256, GlobalsDumpSHA256: plan.GlobalsDumpSHA256,
		DatabaseDumpSHA256: plan.DatabaseDumpSHA256, PostgresImageSHA256: plan.PostgresImageSHA256,
		ProductionSourceContainerID: plan.SourceContainerID, ProductionSourceSystemIdentifier: plan.SourceSystemIdentifier,
		ProductionSourceDatabase: plan.SourceDatabase, ProductionSourceDatabaseOID: plan.SourceDatabaseOID,
		ProductionSourceDatabaseOwnerOID: plan.SourceDatabaseOwnerOID, ProductionSourceDatabaseOwner: plan.SourceDatabaseOwner,
		ProductionSourceGooseWaterline: 41, IsolatedTargetContainerID: plan.IsolatedContainerID,
		IsolatedTargetSystemIdentifier: plan.IsolatedSystemIdentifier, IsolatedTargetNetworkID: plan.IsolatedNetworkID,
		IsolatedTargetDatabase: plan.IsolatedDatabase, IsolatedTargetDatabaseOID: plan.IsolatedDatabaseOID,
		IsolatedTargetImageID: plan.IsolatedImageID, IsolatedTargetRunID: plan.IsolatedRunID,
		ExternalManifestSHA256: plan.ExternalManifestSHA256, AttestationPublicKeySHA256: plan.AttestationPublicKeySHA256,
		ReleaseJournalHeadSHA256: plan.ReleaseJournalHeadSHA256, ExecutionPlanSHA256: hex.EncodeToString(plan.SHA256[:]),
		NotBefore: plan.NotBefore, NotAfter: plan.NotAfter,
	}
}
