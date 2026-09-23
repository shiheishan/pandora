package ca42releasev3

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestContractCoreV2StableFieldsAndExplicitVersions(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	baseline, err := ContractCoreBytes(fixture.manifest, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	text := string(baseline)
	for _, required := range []string{
		"format=pandora-client-auth-00042-release-contract-core-v2\n",
		"release_manifest_format=pandora-client-auth-00042-release-manifest-v3\n",
		"execution_plan_format=pandora-ca42-execution-plan-v2\n",
		"trust_capsule_format=client-auth-00042-trust-capsule-v2\n",
		"release_journal_namespace=client-auth-00042-v2\n",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("contract core missing %q", required)
		}
	}
	for _, forbidden := range []string{"release_journal_head_sha256=", "release_journal_snapshot_sha256=", "execution_plan_sha256=", "signature_b64="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("contract core contains cyclic field %q", forbidden)
		}
	}
	for _, field := range []Field{FieldReleaseJournalHeadSHA256, FieldReleaseJournalSnapshotSHA256, FieldExecutionPlanSHA256} {
		values := append([]string(nil), fixture.values...)
		setManifestValue(t, values, field, fmt.Sprintf("%x", hashFor("changed:"+FieldName(field))))
		changed := parseSignedValues(t, fixture, values)
		changedCore, err := ContractCoreBytes(changed, fixture.now)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(baseline, changedCore) {
			t.Fatalf("cyclic field %s changed stable contract core", FieldName(field))
		}
	}
}

func TestContractCoreV2ChangesForSecuritySemantics(t *testing.T) {
	fixture := newV3Fixture(t, "amd64")
	baseline, err := ContractCoreBytes(fixture.manifest, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	values := append([]string(nil), fixture.values...)
	setManifestValue(t, values, FieldRuntimeClosureManifestSHA256, fmt.Sprintf("%x", hashFor("changed-runtime-closure")))
	changed := parseSignedValues(t, fixture, values)
	changedCore, err := ContractCoreBytes(changed, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(baseline, changedCore) {
		t.Fatal("security-semantic field did not change contract core")
	}
}
