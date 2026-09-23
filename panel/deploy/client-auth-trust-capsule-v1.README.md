# CLIENT-AUTH-00042 trust capsule v1

This candidate fixes the trust root used to validate a signed 41-to-42
attestation. It is deliberately not wired into `migrate.sh`, the release
controller, Goose, or production. `verify-trusted` is read-only: it neither
consumes the nonce nor changes a database.

The command must be executed through a manifest-pinned `pandora-pathtrust exec`
of `client-auth-attestation-v2.sh`. The outer release manifest must pin the
capsule SHA-256 and every `pandora-pathtrust` argument. Passing a capsule path
and its digest from an untrusted caller is not a trust root.

Canonical capsule grammar (fixed order, one LF-terminated line per field):

```text
format=client-auth-00042-trust-capsule-v1
transition=goose-41-to-42
release_id=SAFE_RELEASE_ID
release_run_id=SAFE_UNIQUE_RUN_ID
core_sha256=64_lowercase_hex
core_chain_sha256=64_lowercase_hex
core_device=positive_decimal_st_dev
core_mode=0500
attestation_sha256=64_lowercase_hex
expected_sha256=64_lowercase_hex
public_key_sha256=64_lowercase_hex
external_manifest_sha256=64_lowercase_hex_of_the_complete_READY_JSON
target_system_identifier=positive_decimal_postgresql_system_identifier
target_database_name=canonical_database_name
target_database_oid=positive_decimal_oid
ledger_namespace=client-auth-00042-v1
ledger_directory_sha256=sha256_of_canonical_absolute_ledger_path_without_LF
```

Invocation shape:

```text
pandora-pathtrust exec \
  --path /opt/pandora/deploy/client-auth-attestation-v2.sh \
  --expect-sha256 CORE_SHA --expect-mode 0500 \
  --expect-device CORE_DEVICE --allow-devices SORTED_DEVICE_CSV \
  --expect-chain-sha256 CORE_CHAIN_SHA -- \
  verify-trusted --attestation /root/release/ca42.attestation \
  --public-key /root/release/ca42-public.pem \
  --expected /root/release/ca42.expected \
  --external-manifest /root/release/ca42-external-manifest.json \
  --ledger-dir /var/lib/pandora/release-ledger/client-auth-00042-v1 \
  --capsule /root/release/ca42.trust-capsule \
  --expect-capsule-sha256 CAPSULE_SHA
```

The verifier checks the Ed25519 signature and existing 16-field expected
binding, current validity window, exact retained core descriptor hash and
path-trust identity, all four input artifact hashes, rejection of the known
checked-in placeholder and a unique `READY` external-manifest status, release
identity, target PostgreSQL identity, and the root-owned `0700` ledger path and
namespace. The signed payload's `catalog_manifest_sha256` must equal the
complete external manifest SHA-256, closing the expected-to-external evidence
binding. A
success line is only a trust-validation result. It does not authorize or imply
that Goose ran, that a nonce was consumed, or that a release may proceed.
