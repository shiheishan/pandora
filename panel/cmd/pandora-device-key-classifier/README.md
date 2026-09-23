# Pandora device public-key classifier

Status: **NOT_RELEASE / NOT RELEASE APPROVAL**.

This is a Linux-only, read-only, fail-closed candidate for the
CLIENT-AUTH-00044 maintenance window. Its normative input is
`.ai-company/handoffs/client-auth-00044-classify-backfill-contract-20260731.md`.
The release owner must pin that contract's final SHA-256 after shared edits stop.

The classifier does not connect to PostgreSQL, mutate a row, choose a duplicate
winner, accept private keys, or infer tenant/user ownership. It only consumes a
controlled export whose join result was produced by a separately reviewed
preflight.

## Controlled input

Choose `tsv` or `ndjson`. Every record has exactly:

- `tenant_id`: canonical lowercase hyphenated UUID;
- `id`: canonical lowercase hyphenated device UUID;
- `join_provenance`: exactly `provable`, `orphan`, or `cross_tenant`;
- `key_algorithm`: candidate declaration (`ed25519` or `p256-es256`);
- `public_key`: explicit `hex:<lowercase-hex>` or
  `base64:<canonical-padded-RFC4648-base64>` DER bytes.

The TSV header is exact:

```text
tenant_id	id	join_provenance	key_algorithm	public_key
```

`join_provenance=provable` means the exporter already proved the frozen
tenant/user composite join. The classifier cannot prove that database fact and
does not turn a caller assertion into release approval.

Unknown/extra/missing fields, invalid UUID grammar, duplicate device IDs,
oversized sources/lines, and unknown provenance abort the batch without an
artifact. A malformed/noncanonical key, unknown or mismatched key algorithm, or
oversized key is a per-row `unprovable_key` classification.

For a provable join, the classifier:

1. strictly decodes the declared key encoding;
2. calls `x509.ParsePKIXPublicKey`;
3. calls `x509.MarshalPKIXPublicKey` and requires byte-for-byte equality;
4. accepts only Ed25519 or ECDSA NIST P-256 matching the declared algorithm;
5. computes `SHA-256(canonical DER SPKI)`.

Candidate fingerprints are grouped by `(tenant_id,fingerprint)`. Every member
of a same-tenant collision becomes `unprovable_key` with no winner and no
published trusted fingerprint. The same fingerprint in different tenants is
allowed, matching the frozen 00045 uniqueness key.

## Root-only artifact and exact detached stdout

The classifier accepts exactly ten ordered argv tokens after the executable:

```text
pandora-device-key-classifier \
  -format ndjson \
  -artifact-dir-fd 4 \
  -artifact-name devices.classified.json \
  -artifact-hmac-key-id artifact-2026-01 \
  -artifact-hmac-key-fd 3
```

Each flag must appear exactly once and in that order. `-format` is exactly
`tsv` or `ndjson`. Both FD values are canonical decimal integers greater than
or equal to 3: digits only, with no sign, whitespace, leading zero, or alternate
numeric spelling. The artifact-directory FD and artifact-key FD must differ.
Positional arguments, paths, environment-provided key material or key IDs, and
the legacy `-hmac-key-fd` flag are rejected as invalid arguments.

The artifact HMAC key ID is public CLI metadata matching
`^[a-z0-9][a-z0-9._-]{0,63}$`. The key material is accepted only through the
inherited artifact-key FD and must be exactly 32 bytes in a root-owned,
single-link, read-only regular file with no group or other permission bits. The
classifier never opens a key path. Source bytes are read only from inherited
stdin. The root runner remains responsible for validating the source and the
complete ancestor/path trust of the already-open artifact directory FD. The
classifier rechecks that descriptor is a root-owned directory with no
group/other permission. On Linux it creates a 0600 `O_NOFOLLOW|O_EXCL`
temporary file, fsyncs it, and publishes with `renameat2(RENAME_NOREPLACE)`.

The artifact HMAC is HMAC-SHA-256 over the framed message:

```text
ASCII("pandora-client-auth-00044-classification-artifact-hmac-v1") || 0x00
|| artifact_hmac_key_id_length u16be || artifact_hmac_key_id ASCII bytes
|| artifact_format_length u16be || artifact_format ASCII bytes
|| artifact_length u64be || exact_artifact_bytes
```

`artifact_format` is exactly `pandora-device-key-classification-v1`. The
artifact SHA-256 is independently computed over the same exact artifact bytes.
A raw `HMAC(key, artifactBytes)` is legacy and non-authorizing.

The classifier first publishes the root-only artifact through the validated
directory FD. Only after artifact publication succeeds does it write stdout.
If the detached stdout write fails or short-writes, the process exits nonzero;
the root runner must quarantine or remove that non-authorizing candidate before
retry and must never infer success from artifact-file existence alone.
Stdout is exactly one UTF-8 canonical JSON object followed by one LF, with this
complete field order:

```text
manifest_format,artifact_hmac_version,artifact_hmac_key_id,artifact_format,
source_format,source_length,artifact_length,source_sha256,artifact_sha256,
artifact_hmac_sha256,input_rows,output_rows,provable,orphan,cross_tenant,
unprovable_key
```

`manifest_format` is exactly
`pandora-client-auth-00044-classifier-detached-v1`. The detached JSON contains
no BOM, CR, unnecessary whitespace, extra field, raw UUID, SPKI, fingerprint,
key material, `manifest_hmac_sha256`, or
`legacy_raw_artifact_hmac_sha256`. Errors contain only stable denial codes and
an optional line number.

The controlled artifact contains raw tenant/device UUIDs and complete trusted
fingerprints. It remains root-only sensitive mapping and must not be sent to
consoles, ordinary logs, tickets, or ordinary CI artifacts. Artifact HMAC and
evidence row-set HMAC are separate protocols; neither substitutes for the
other.

## Limits and release boundary

- Maximum source size: 16 MiB; maximum line size: 64 KiB; maximum records:
  100,000; maximum decoded DER: 4096 bytes.
- No database join validation, revocation, backfill, migration, or writer.
- No signature or private-key possession proof.
- No assertion that legacy `devices.public_key` is canonical SPKI.
- The old CLI, legacy raw-HMAC output, and dual legacy/new emission are rejected
  and must not enter a verifier, receipt, release gate, or database adapter.
- Classifier exit 0, artifact publication, detached JSON, and framed artifact
  HMAC establish only a candidate artifact. They do not authorize packaging,
  migration, production execution, or Client-route enablement.
- Signed release-manifest verification, exact contract/classifier/verifier
  identity, independent artifact verification, real Linux-root path trust,
  PostgreSQL 18 integration, equal-volume performance, and independent
  Database/Security approval remain mandatory. Until all such gates pass, the
  status remains **NOT_RELEASE / NOT RELEASE APPROVAL**.
