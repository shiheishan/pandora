# pandora-cic-journal

`pandora-cic-journal` is a Linux `amd64`/`arm64`, Go-standard-library source
candidate for the CLIENT-AUTH-00043 pre-CIC cleanup journal. It is deliberately
not wired into the runner, migration, release manifest, or production entry
point.

The caller supplies a release-manifest-fixed absolute `--journal-root`, its
exact `--expected-root-device`, and a strictly sorted `--allow-devices`
allowlist. The caller cannot supply a journal pathname. `create-intent` derives
the journal directory
`<root>/<run_id>/<candidate>.<CSPRNG journal_id>.journal`; append commands
accept only the same safe `run_id` plus the generated journal basename. The
trusted root and run directory must already exist.

Every path component is opened relative to a retained directory descriptor
with `openat2(2)` and
`RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS`. There is no
`openat(2)` compatibility fallback. Ancestors must be root-owned directories
without group/world write bits and must be on an allowed device. Journal files
must remain root-owned regular files with mode `0600`, one hard link, the same
device/inode identity, and the expected size across each operation.

The journal is a manifest over one immutable file per phase:
`000.intent.record`, `010.catalog.record`, optional `020.drop.record`, and
`030.close.record`. Its `journal_sha256` is the SHA-256 of the ordered segment
names, each segment SHA-256, and the hash-chain head. No published record is
ever opened for append, truncate, or overwrite.

Intent publication builds and syncs a complete CSPRNG-named directory, then
publishes the directory with `renameat2(RENAME_NOREPLACE)` and syncs its
parent. Later phases take an exclusive lock on the journal directory, build a
complete CSPRNG-named record in the run directory with
`O_CREAT|O_EXCL|O_NOFOLLOW`, `fdatasync`, and `fsync`, then publish that file
into the journal directory with `renameat2(RENAME_NOREPLACE)` and sync the
journal directory. A short write, ENOSPC, kill, or pre-rename sync failure can
therefore leave only an unpublished stage outside the journal. If the record
rename succeeds but the following directory sync fails, the same command can
be retried with the prior manifest SHA: exact record bytes are recognized,
re-synced, and returned as `recovered=true`. A different retry fails closed.

The grammar rejects non-UTF-8, CR, NUL, missing final LF, blank/overlong lines,
unknown, missing, duplicate, or reordered keys, non-canonical numbers and
hashes, illegal transitions, and bytes after a terminal `closed` record.
`record_sha256` for intent is SHA-256 of its canonical record body (through
the status line, including LF). Later record hashes are SHA-256 of the previous
lowercase ASCII `record_sha256` immediately followed by the new canonical
record body. The immutable segment names provide the only accepted record
ordering.

Commands:

```text
pandora-cic-journal create-intent [frozen identity and expected hash flags]
pandora-cic-journal append-catalog --run-id ID --journal BASENAME ...
pandora-cic-journal append-drop --run-id ID --journal BASENAME ...
pandora-cic-journal append-close --run-id ID --journal BASENAME ...
```

All commands require EUID 0. There is no `exec`, SQL, catalog-file, arbitrary
journal-path, truncate, overwrite, or delete command. Catalog/drop/close inputs
are assertions from the still-required advisory-lock/live-database layer; this
tool only persists and validates their canonical provenance chain. Therefore a
successful command is not authorization to execute CIC or DROP and is not
integration or release approval.
