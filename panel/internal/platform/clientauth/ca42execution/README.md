# CA42 execution plan v1

`execution.plan` is a root-owned, mode `0400`, canonical sidecar for the
`client-auth-00042` migration. It is not an independent authority object. The
Ed25519-signed release manifest v2 pins the SHA-256 of the complete plan, and
the compiled root quorum pins the complete release manifest.

The trust direction is deliberately acyclic:

```text
compiled root keyset -> authority descriptor -> signed release manifest
                     -> execution.plan -> retained migration artifacts
```

The caller may provide only `execute --attempt-id TOKEN`. Paths, Docker
endpoints, device allowlists, clocks, hashes, and output locations are not plan
inputs and are not accepted through argv or environment variables.

## Canonical envelope

- UTF-8 without BOM, CR, or NUL.
- Exactly the fields emitted by `CanonicalBytes`, in the compiled order.
- Exactly one non-empty value per field and one final LF.
- Maximum size: 64 KiB.
- Lowercase non-zero 64-character SHA-256 values.
- Validity interval `[not_before_epoch, not_after_epoch)`, at most one hour.
- Source and isolated container/system identities must differ; database names
  must match; source Goose waterline is exactly 41.
- The isolated image ID must be `sha256:` plus the pinned PostgreSQL image
  digest, and the migration file digest must equal the compiled frozen digest.

`Parse` validates the plan's complete SHA against the signed release pin.
`BindRelease` then compares every overlapping release, artifact, database,
identity, journal-head, and validity field. A plan from another release, run,
attempt, architecture, dump, image, or time window is rejected.

The journal bound by this plan must be the future CA42 journal v2. Its PREPARED
record binds `ca42release.ContractCoreSHA256`, not the complete signed release
manifest. Binding the complete manifest would be impossible because the
manifest pins this plan and this plan pins the journal. The contract core omits
the journal head, plan digest, and signature while retaining all stable release
identity and artifact fields.

## Current boundary

The current root runner performs read-only contract verification only. It now
parses the fixed `trust.capsule` and cross-binds its complete hash and internal
core/attestation/expected/key/external/source/ledger identities to this plan.
It does not yet retain and verify every referenced artifact FD, reserve the
authority ledger, consume a nonce, execute Docker/PostgreSQL/Goose, advance the
release journal, or clean up retained resources. Those actions remain
fail-closed until journal v2 and the retained execution session are connected
and accepted on native Linux.
