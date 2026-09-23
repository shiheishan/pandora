package ca42artifactsv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42credential"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42gooseinfo"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42runtimeclosure"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

type sidecarFixture struct {
	credential    ca42credential.Descriptor
	runtime       ca42runtimeclosure.Manifest
	goose         ca42gooseinfo.BuildInfo
	storage       ca42storage.Descriptor
	credentialRaw []byte
	runtimeRaw    []byte
	gooseRaw      []byte
	storageRaw    []byte
}

type storageFixtureBuilder func(*testing.T, map[string]string, string, string, sidecarFixture) (ca42storage.Descriptor, []byte)

func newSidecarFixture(t *testing.T, base map[string]string, architecture, label string, now time.Time) sidecarFixture {
	return newSidecarFixtureWithStorage(t, base, architecture, label, now, false, nil)
}

func newSidecarFixtureWithStorage(t *testing.T, base map[string]string, architecture, label string, now time.Time, dynamicWindow bool, build storageFixtureBuilder) sidecarFixture {
	t.Helper()
	credential, credentialRaw := sidecarCredential(t, base, label, now, dynamicWindow)
	base["credential_source_descriptor_sha256"] = hex.EncodeToString(credential.SHA256[:])
	goose, gooseRaw := sidecarGoose(t, base, architecture, label)
	base["goose_build_info_sha256"] = hex.EncodeToString(goose.SHA256[:])
	runtimeClosure, runtimeRaw := sidecarRuntime(t, base, architecture, label)
	base["runtime_closure_manifest_sha256"] = hex.EncodeToString(runtimeClosure.SHA256[:])
	partial := sidecarFixture{credential: credential, runtime: runtimeClosure, goose: goose,
		credentialRaw: credentialRaw, runtimeRaw: runtimeRaw, gooseRaw: gooseRaw}
	var storage ca42storage.Descriptor
	var storageRaw []byte
	if build == nil {
		storage, storageRaw = sidecarStorage(t, base, architecture, label)
	} else {
		storage, storageRaw = build(t, base, architecture, label, partial)
	}
	base["artifact_storage_descriptor_sha256"] = hex.EncodeToString(storage.SHA256[:])
	return sidecarFixture{credential: credential, runtime: runtimeClosure, goose: goose, storage: storage,
		credentialRaw: credentialRaw, runtimeRaw: runtimeRaw, gooseRaw: gooseRaw, storageRaw: storageRaw}
}

func sidecarCredential(t *testing.T, base map[string]string, label string, now time.Time, dynamicWindow bool) (ca42credential.Descriptor, []byte) {
	t.Helper()
	secret := bytes.Repeat([]byte("s"), ca42credential.MinCredentialBytes)
	key := sha256.Sum256([]byte("credential-key:" + label))
	notBefore, notAfter := time.Unix(1700000100, 0).UTC(), time.Unix(1700003400, 0).UTC()
	if dynamicWindow {
		notBefore, notAfter = now.UTC(), now.Add(45*time.Minute).UTC()
	}
	raw := ca42credential.Descriptor{
		CommitmentAlgorithm: ca42credential.CommitmentHMACKeyringV1,
		CommitmentKeyID:     "keyring-" + label,
		ReleaseID:           base["release_id"], ReleaseRunID: base["release_run_id"], AttemptID: base["attempt_id"],
		SourceContainerID: base["source_container_id"], SourceSystemIdentifier: base["source_system_identifier"],
		SourceDatabase: base["source_database_name"], SourceDatabaseOID: base["source_database_oid"],
		SourceDatabaseOwner: base["source_database_owner_name"], SourceDatabaseOwnerOID: base["source_database_owner_oid"],
		CredentialSizeBytes: uint64(len(secret)), NotBefore: notBefore, NotAfter: notAfter,
	}
	commitment, err := ca42credential.HMACCommitment(secret, key[:], raw)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{
		ca42credential.Format, ca42credential.Kind, ca42credential.CredentialName, ca42credential.CredentialFormat,
		raw.CommitmentAlgorithm, raw.CommitmentKeyID, commitment, ca42credential.Mode,
		strconv.FormatUint(raw.CredentialSizeBytes, 10), ca42credential.Delivery,
		raw.ReleaseID, raw.ReleaseRunID, raw.AttemptID, raw.SourceContainerID, raw.SourceSystemIdentifier,
		raw.SourceDatabase, raw.SourceDatabaseOID, raw.SourceDatabaseOwner, raw.SourceDatabaseOwnerOID,
		strconv.FormatInt(raw.NotBefore.Unix(), 10), strconv.FormatInt(raw.NotAfter.Unix(), 10),
	}
	data, err := ca42credential.CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	parsed, err := ca42credential.Parse(data, digest, now)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, data
}

func sidecarGoose(t *testing.T, base map[string]string, architecture, label string) (ca42gooseinfo.BuildInfo, []byte) {
	t.Helper()
	machine := map[string]string{"amd64": "EM_X86_64", "arm64": "EM_AARCH64"}[architecture]
	mainSum := ca42gooseinfo.RequiredMainModuleSum
	values := []string{
		ca42gooseinfo.Format, "goose", base["goose_binary_sha256"], "1048576", architecture,
		ca42gooseinfo.RequiredGoVersion, ca42gooseinfo.RequiredCommandPath, ca42gooseinfo.RequiredMainModulePath,
		ca42gooseinfo.RequiredGooseVersion, mainSum, "none", "1", graphHash("goose-deps:" + label),
		"1", graphHash("goose-settings:" + label), "false", "exe", "true", "git",
		ca42gooseinfo.RequiredVCSRevision, ca42gooseinfo.RequiredVCSTime, "false", "ELFCLASS64", "ELFDATA2LSB",
		"ELFOSABI_NONE", "ET_EXEC", machine, "absent", "0", "absent", "absent",
	}
	data, err := ca42gooseinfo.CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	parsed, err := ca42gooseinfo.Parse(data, digest, base["goose_binary_sha256"], architecture)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, data
}

func sidecarRuntime(t *testing.T, base map[string]string, architecture, label string) (ca42runtimeclosure.Manifest, []byte) {
	t.Helper()
	values := []string{
		ca42runtimeclosure.Format, base["release_id"], base["release_run_id"], base["attempt_id"], architecture,
		ca42runtimeclosure.EntryCount, graphHash("root-runner:" + label), graphHash("root-runner-info:" + label),
		base["pathtrust_binary_sha256"], graphHash("pathtrust-info:" + label),
		base["manifest_verifier_sha256"], graphHash("manifest-verifier-info:" + label),
		base["preflight_runner_sha256"], graphHash("preflight-info:" + label),
		base["migration_runner_sha256"], graphHash("migration-info:" + label),
		base["goose_binary_sha256"], base["goose_build_info_sha256"],
		base["docker_client_sha256"], "none",
	}
	data, err := ca42runtimeclosure.CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	parsed, err := ca42runtimeclosure.Parse(data, digest, architecture)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, data
}

type storageRole struct{ role, kind, name, mode, hashField string }

var sidecarAttemptRoles = [...]storageRole{
	{"external_manifest", "manifest", "external-manifest.json", "0400", "external_manifest_sha256"},
	{"pathtrust_binary", "executable", "pathtrust", "0500", "pathtrust_binary_sha256"},
	{"attestation_public_key", "data", "attestation-public-key", "0400", "attestation_public_key_sha256"},
	{"credential_source_descriptor", "data", "credential-source.descriptor", "0400", "credential_source_descriptor_sha256"},
	{"runtime_closure_manifest", "manifest", "runtime-closure.manifest", "0400", "runtime_closure_manifest_sha256"},
	{"manifest_verifier", "executable", "manifest-verifier", "0500", "manifest_verifier_sha256"},
	{"preflight_runner", "executable", "preflight-runner", "0500", "preflight_runner_sha256"},
	{"migration_runner", "executable", "migration-runner", "0500", "migration_runner_sha256"},
	{"goose_binary", "executable", "goose", "0500", "goose_binary_sha256"},
	{"goose_build_info", "manifest", "goose-build-info.manifest", "0400", "goose_build_info_sha256"},
	{"migration_manifest", "manifest", "migration-set.manifest", "0400", "migration_set_sha256"},
	{"globals_dump", "dump", "globals.dump", "0400", "globals_dump_sha256"},
	{"database_dump", "dump", "database.dump", "0400", "database_dump_sha256"},
}

var sidecarMigrationNames = [...]string{
	"00001_foundation.sql", "00002_identity.sql", "00003_catalog_subscription.sql", "00004_billing_ledger.sql",
	"00005_node_fabric.sql", "00006_metering.sql", "00007_client_delivery.sql", "00008_ops_marketing.sql",
	"00009_security_audit.sql", "00010_seed_rbac.sql", "00011_app_role.sql", "00012_audit_node_actor.sql",
	"00013_xboard_node_protocol.sql", "00014_subscription_proxy_uuid.sql", "00015_node_metrics.sql", "00016_node_kernel.sql",
	"00017_node_routing.sql", "00018_subscription_delivery.sql", "00019_subscription_token_vault.sql", "00020_change_notify.sql",
	"00021_notify_fix_tables.sql", "00022_notify_exclude_messages.sql", "00023_notification_seed.sql", "00024_device_limit_modes.sql",
	"00025_audit_ip_vault.sql", "00026_ip_cluster_window.sql", "00027_drop_cluster_idx.sql", "00028_commission_settings.sql",
	"00029_commission_defaults.sql", "00030_mail_settings.sql", "00031_pandora_brand.sql", "00032_revenue_report_adjustments.sql",
	"00033_server_node_split.sql", "00034_node_admin_concurrency.sql", "00035_catalog_authoring.sql", "00036_order_reservations.sql",
	"00037_idempotency_runtime_hardening.sql", "00038_idempotency_resource_binding.sql", "00039_bound_idempotency_success.sql",
	"00040_order_release_and_late_suspense.sql", "00041_dashboard_read_models.sql", "00042_client_auth_expand.sql",
}

func sidecarStorage(t *testing.T, base map[string]string, architecture, label string) (ca42storage.Descriptor, []byte) {
	t.Helper()
	root := "/run/pandora/ca42/" + base["attempt_id"]
	entries := make([]string, 0, ca42storage.AttemptEntryCount+ca42storage.MigrationEntryCount+ca42storage.MinRuntimeEntryCount)
	add := func(scope, role, kind, path, mode, content string, size, device uint64) {
		ordinal := len(entries) + 1
		verity := graphHash(fmt.Sprintf("storage-verity:%s:%d", label, ordinal))
		entries = append(entries, fmt.Sprintf("entry=%06d|%s|%s|%s|%s|%s|0|0|1|%d|%d|%d|%d|%s|%s",
			ordinal, scope, role, kind, hex.EncodeToString([]byte(path)), mode, size, device, 1000+ordinal, 2000+ordinal, content, verity))
	}
	for _, role := range sidecarAttemptRoles {
		size := uint64(128 + len(entries))
		if role.role == "globals_dump" {
			size = 1024
		}
		if role.role == "database_dump" {
			size = 1073741824
		}
		add("attempt", role.role, role.kind, root+"/"+role.name, role.mode, base[role.hashField], size, 10)
	}
	for index, name := range sidecarMigrationNames {
		content := graphHash(fmt.Sprintf("migration:%s:%d", label, index+1))
		if index == len(sidecarMigrationNames)-1 {
			content = base["client_auth_00042_sha256"]
		}
		add("migration", "migration_sql", "sql", root+"/migrations/"+name, "0400", content, uint64(256+index), 11)
	}
	add("system", "bash", "elf", "/usr/bin/bash", "0755", base["bash_binary_sha256"], 4096, 20)
	add("system", "docker", "elf", "/usr/bin/docker", "0755", base["docker_client_sha256"], 4097, 20)
	loaderPath := map[string]string{
		"amd64": "/lib64/ld-linux-x86-64.so.2",
		"arm64": "/lib/ld-linux-aarch64.so.1",
	}[architecture]
	add("system", "runtime_loader", "elf", loaderPath, "0755", graphHash("loader:"+label), 4098, 20)
	add("system", "runtime_library", "elf", "/usr/lib/libc.so.6", "0644", graphHash("libc:"+label), 4099, 20)
	inventory := strings.Join(entries, "\n") + "\n"
	inventoryDigest := sha256.Sum256([]byte(inventory))
	headers := []string{
		"format=" + ca42storage.Format, "profile=" + ca42storage.Profile,
		"release_id=" + base["release_id"], "release_run_id=" + base["release_run_id"], "attempt_id=" + base["attempt_id"],
		"architecture=" + architecture, "attempt_root_hex=" + hex.EncodeToString([]byte(root)),
		"content_hash_algorithm=sha256", "verity_hash_algorithm=sha256",
		"attempt_entry_count=" + strconv.Itoa(ca42storage.AttemptEntryCount),
		"migration_entry_count=" + strconv.Itoa(ca42storage.MigrationEntryCount),
		"runtime_entry_count=" + strconv.Itoa(ca42storage.MinRuntimeEntryCount),
		"entry_count=" + strconv.Itoa(len(entries)), fmt.Sprintf("inventory_sha256=%x", inventoryDigest),
	}
	data := []byte(strings.Join(headers, "\n") + "\n" + inventory)
	digest := sha256.Sum256(data)
	parsed, err := ca42storage.Parse(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, data
}
