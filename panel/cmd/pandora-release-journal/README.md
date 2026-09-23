# Pandora release journal v1

`pandora-release-journal` is a Linux-only, root-only durability primitive for a
Pandora release controller. It records what the controller has durably prepared
or observed. It deliberately cannot execute SQL, Goose, systemd, shell commands,
network requests, deletion, rollback, or deployment.

Supported targets are Linux `amd64` and `arm64`. Other targets build a stub that
fails closed.

## Commands

- `prepare` freezes the release, machine, filesystem, database, migration,
  ingress, writer, backup-controller and health-check identities. The caller
  supplies a stable `--attempt-id`; the primitive generates the journal ID.
- `inspect` validates the entire immutable chain and prints its last durable
  state without repairing or mutating it.
- `advance` accepts only the next legal state (or `RECOVERY_REQUIRED`) using
  `--expect-journal-sha256` CAS and a trusted inherited `--evidence-fd`.

All commands require EUID 0 plus `--journal-root`,
`--expected-root-device`, and a strictly increasing `--allow-devices` list.
The journal root must be an absolute canonical path whose complete ancestry is
root-owned, not group/world-writable, and on allowed devices.

The release controller must open evidence as a root-owned, mode `0600`,
single-link regular file and pass its inherited descriptor. Evidence is hashed
and sized; its content is not copied into the journal. The descriptor is read
with before/after identity and metadata checks and a 1 MiB limit.

## State graph

```text
PREPARED
 -> ISOLATION_ATTEMPTED -> ISOLATED
 -> BACKUP_ATTEMPTED -> BACKUP_VERIFIED
 -> LAYOUT_SWITCH_ATTEMPTED -> LAYOUT_SWITCHED
 -> ADMISSION_ATTEMPTED -> ADMISSION_VERIFIED
 -> MIGRATION_ATTEMPTED -> MIGRATED
 -> WRITERS_START_ATTEMPTED -> WRITERS_READY
 -> EXPOSURE_ATTEMPTED -> COMMITTED
```

Every nonterminal state may instead transition to terminal
`RECOVERY_REQUIRED`. `COMMITTED` and `RECOVERY_REQUIRED` have no outgoing edge.
The journal primitive does not decide whether evidence proves a state; that is
the separately reviewed controller's responsibility.

## Durability and retry contract

- Records use fixed-order UTF-8/LF canonical fields and immutable, mode `0600`,
  single-link files.
- Each transition hash binds the previous record hash. The journal manifest
  binds ordered segment names, individual segment hashes, and the chain head.
- Publication writes into bounded CSPRNG temporary objects, fully synchronizes
  them, then publishes deterministic recovery-stage names and final records with
  `renameat2(RENAME_NOREPLACE)` plus file and directory `fsync` barriers.
- `prepare` discovers journals only from `attempt-id`; an exact identity retry
  re-synchronizes and reports `recovered=true`. A different identity fails.
- `advance` serializes on the journal directory. A post-rename retry succeeds
  only when the prior prefix matches the supplied CAS and the already-published
  record is byte-for-byte identical to the requested record.
- `openat2` uses `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS |
  RESOLVE_NO_MAGICLINKS`; there is no legacy path-opening fallback.

## Integration status

This directory is an independent primitive. It must not be added to the release
artifact or wired into the production controller until Linux root runtime fault
tests, an independent security review, and the higher-level release recovery
gates all pass.
