# Pandora client-auth 00044 root runner

This command is the Linux-only, root-owned supervisor for the non-authorizing
00044 classifier artifact. It does not execute a database migration and its
success receipt always contains `authorization=NONE`.

## Security boundary

The runner requires Linux `amd64` or `arm64`, EUID 0, `openat2`, `/proc`, and a
root-owned trust root whose complete ancestor chain is not group/world
writable. The runner, classifier and verifier must be regular root-owned
`0500` files with one hard link. Immutable inputs and 32-byte keys must be
root-owned `0400` files with one hard link. The staging and publication roots
must already exist beneath the trust root as separate root-owned `0700`
directories on the same filesystem.

Configuration contains only paths, SHA-256 identities and bounded durations.
Key bytes are never accepted through argv or the environment. The two key
paths, IDs, identity pins and actual 32-byte values must all differ.

The signed release manifest is pinned twice: by its full SHA-256 and by the
SHA-256 of its raw Ed25519 public key. It also pins the architecture, exact
runner/classifier/verifier ELF identities, source/artifact identities and the
canonical 24-field expectations, 16-field detached manifest and 25-field
receipt contract.

## Child descriptors

Classifier:

| FD | Object |
|---:|---|
| 0 | retained source |
| 1 | bounded private detached capture |
| 2 | bounded private diagnostic capture |
| 3 | artifact HMAC key |
| 4 | private candidate directory |
| 5 | retained classifier ELF, executed through `/proc/self/fd/5` |

Verifier:

| FD | Object |
|---:|---|
| 3..29 | explicitly closed padding |
| 30 | retained source |
| 31 | retained candidate artifact |
| 32 | canonical detached manifest |
| 33 | canonical release expectations |
| 34 | artifact HMAC key |
| 35 | evidence HMAC key |
| 36 | retained verifier ELF, executed through `/proc/self/fd/36` |

Both children run with a clean fixed environment, a dedicated process group,
`Pdeathsig=SIGKILL`, bounded stdout/stderr drains and timeout-driven process
group termination. No child output is forwarded to the caller.

## Publication and recovery

The classifier writes only below `staging/active/run.<random>/candidate`. The
runner compares its detached stdout byte-for-byte, opens and pins the artifact,
then runs the independent verifier. Only after verifier exit 0 and an exact
receipt match does the runner move the artifact into the private bundle, write
and fsync the receipt, and atomically publish the whole directory with
`RENAME_NOREPLACE`.

Before each run, an exclusive staging lock is acquired. Strict stale
`active/run.<32-lower-hex>` directories are atomically moved to the private
quarantine. Unexpected names, metadata or collisions fail closed. Cleanup uses
retained directory descriptors and an allowlist of exact names; there is no
recursive pathname deletion.

An existing publication is accepted only when it is an exact five-file bundle
whose artifact hash/length plus detached, expectations, signed release manifest
and receipt bytes all match. Before an idempotent success receipt, the runner
also fsyncs the existing bundle and publication parent so a retry repairs a
previous rename-success/parent-fsync-failure window. A crash before publication
leaves a private orphan; a crash after the directory rename leaves a complete
verified bundle that must still pass this retry durability barrier.

## Verification

Local Windows/static and cross-build gate:

```sh
./deploy/pandora-client-auth-00044-root-runner_mock_test.sh
```

Reproducible release-profile binaries use Go 1.26.5 and the exact flags below;
an unstripped default `go build` is a different build profile and therefore has
a different SHA-256:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath \
  -ldflags='-s -w' -o pandora-client-auth-00044-root-runner-amd64 \
  ./cmd/pandora-client-auth-00044-root-runner
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -buildvcs=false -trimpath \
  -ldflags='-s -w' -o pandora-client-auth-00044-root-runner-arm64 \
  ./cmd/pandora-client-auth-00044-root-runner
```

Native Linux-root acceptance requires a separately provisioned signed fixture:

```sh
./deploy/test-client-auth-00044-root-runner-linux-root.sh
```

The native script returns `NOT_RUN`/77 when Linux, root, or any required fixture
identity is unavailable. Cross-building an ELF is not native runtime evidence.
As of this source checkpoint, native Linux-root, PostgreSQL 18, VPS and
production execution remain `NOT_RUN`.
