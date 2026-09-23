# CA42 V3 control-bundle contract

This package is the only basename and root-path source of truth shared by the
future V3 producer and the production root runner. Legacy CA42 v2 names are not
aliases and there is no parse-fallback path.

`RootPath`, every ancestor below `/var/lib`, and the attempt directory are
root:root directories with no symlink or magic-link traversal and no
group/world write permission. `RootPath` and the attempt directory are mode
`0700`; their retained device/inode/mount identity must remain stable. The
producer publishes one attempt directory whose basename passes
`ValidateAttemptID`. It contains exactly the seven ordered entries returned by
`Entries()`, no extras: six immutable data files with mode `0400` and one immutable
attestation-core executable with mode `0500`. Every regular file has UID/GID 0,
link count 1, a positive size no greater than its frozen `MaxBytes`, and a
stable identity across readback.

The producer works through retained trusted parent dirfds. It creates a private
root-owned `0700` staging child with exclusive no-follow operations, writes
content-only temporary files, applies final root:root ownership and frozen
mode, fsyncs and reverifies every file and the staged directory, then uses
`renameat2(RENAME_NOREPLACE)` and fsyncs the parent. It re-opens and reverifies
the published identities. An existing target returns `EEXIST` and is never
filled, overwritten, deleted, renamed, quarantined, merged, or interpreted as
an older layout by this producer.

The 13 execution roles, including the only external manifest, live in the
storage-v2 inventory rooted at `/run/pandora/ca42/<attempt-id>`. The cross-root
commit order is: (1) completely publish and fs-verity-seal inventory, (2)
atomically publish the complete control bundle, (3) publish the signed authority
descriptor last as the commit point. Authority visibility therefore means both
roots are already complete. The runner may bounded-bootstrap the fixed
`external_manifest` role before release -> plan -> storage establishes the full
inventory lease, but it must later prove that the retained role has the same
identity, digest, and complete bytes. No other copy and no fallback is allowed.

This file freezes the layout contract only. It does not claim that the producer,
production opener, complete ArtifactSet parser, fs-verity E2E, or admission path
has been implemented or released.
