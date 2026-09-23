# pandora-pathtrust

Linux-only release-artifact trust helper. It walks an absolute path from `/`
using retained directory file descriptors, prefers
`openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS)`, and falls
back to one-component-at-a-time `openat(O_NOFOLLOW)` only when `openat2` is not
implemented by the kernel.

Policy:

- every ancestor and the target must be owned by UID 0;
- no ancestor or target may be group/world writable;
- the target must be a regular non-symlink file with link count exactly one;
- optional SHA-256 comparison is performed on the already-open target fd.

CLI contract:

```text
pandora-pathtrust check --path /opt/pandora/deploy/tool --expect-sha256 HEX --expect-mode 0500 --expect-device DEV --allow-devices DEV[,DEV...] --expect-chain-sha256 HEX
pandora-pathtrust exec  --path /opt/pandora/deploy/tool --expect-sha256 HEX --expect-mode 0500 --expect-device DEV --allow-devices DEV[,DEV...] --expect-chain-sha256 HEX -- ARG...
```

`check` prints one `PANDORA_PATHTRUST_V1 ...` record; the path is encoded as
`path_hex` so whitespace cannot corrupt field parsing. This fd is process-local,
so callers that need race-resistant execution must use `exec`; `exec` maps the
verified file to child fd 3 and runs it with the fixed `/bin/bash` interpreter.
Production `exec` requires the manifest-bound SHA-256, exact mode `0500`,
exact target `st_dev`, a strictly sorted manifest allowlist covering every
ancestor device/mount, the manifest-bound SHA-256 of the ordered ancestor
device/inode chain, and successful `openat2`; it never uses the compatibility
`openat` fallback. The child receives
`PANDORA_TRUSTED_FD=3`, `PANDORA_PATHTRUST_VERSION=1`,
`PANDORA_TRUSTED_SHA256`, `PANDORA_TRUSTED_CHAIN_SHA256`,
`PANDORA_TRUSTED_DEVICE`, and `PANDORA_TRUSTED_MODE`. These values describe
the same retained artifact descriptor that was checked before execution. Its
environment is rebuilt from a fixed
`PATH/HOME/LANG/LC_ALL` plus the narrowly scoped
`PANDORA_CLIENT_AUTH_00043_*` contract; shell startup variables are not
inherited.

Exit codes: `0` trusted/success, `64` CLI misuse, `65` trust-policy or digest
failure, `70` internal/platform failure. `exec` otherwise propagates the child
exit code.

Supported release targets: Linux `amd64` and `arm64`.
