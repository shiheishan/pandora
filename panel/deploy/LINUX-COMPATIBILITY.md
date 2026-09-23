# Pandora Panel Linux compatibility

## Supported hosts

- CPU: `amd64`/`x86_64` and `arm64`/`aarch64`.
- Ubuntu: 22.04 through 26.04.
- Debian: 12 through 14.
- Init system: systemd.
- Container runtime: Docker Engine with the Compose v2 plugin.

Application binaries are built with `CGO_ENABLED=0`. They do not depend on the
host glibc version. PostgreSQL and Valkey run from multi-architecture container
images and keep their ports bound to `127.0.0.1`.

Ubuntu interim releases 23.xx and 25.xx are accepted by the platform detector,
but production installations should prefer an LTS release. The upper bounds are
intentional: a newer unvalidated distribution fails closed until its deployment
matrix has been tested.

## Build both architectures

Run on a controlled builder with Go 1.26 or later:

```bash
bash deploy/build-release.sh
```

The command produces `linux_amd64` and `linux_arm64` archives under `dist/`,
plus an archive checksum and a separate `*.manifest.sha256` release-manifest
digest for each architecture. Publish the expected digests through a trusted
release channel separate from the archive storage.

## Host preflight

```bash
sudo bash deploy/preflight-linux.sh
```

The check fails closed for unsupported distributions, 32-bit CPUs, a missing
systemd environment, an unavailable Docker daemon, or a missing Compose v2
plugin.

## Install verified binaries

Verify the archive checksum, obtain the manifest digest from the trusted
release channel, extract as root into a root-owned non-writable staging tree,
then run:

```bash
sudo env PANDORA_RELEASE_MANIFEST_SHA256='<trusted 64-hex digest>' \
  /absolute/path/to/extracted-release/deploy/install-linux-binaries.sh \
  /absolute/path/to/extracted-release
```

The installer refuses a mutable or non-root-owned release tree, verifies the
out-of-band manifest digest and every packaged file before loading package code,
and runs the Linux dependency preflight before writing. It serializes installs,
stops the backup timer/service, stages the complete file set, preserves a
verified rollback set, and restores the whole previous set if commit fails.
Environment files and database data are never included in release archives.
The timer remains disabled on a first install; configure and test the independent
checkpoint hook before enabling it. This installer is only for a first install
or a reviewed binary-only change. Any release that includes schema or writer
contract changes must use the maintenance-window controller below.

## Schema-changing production release

Catalog, idempotency, and other writer/schema changes must use the bundled
maintenance-window controller. Rolling, blue-green, and mixed-version writer
deployments are rejected by policy.

```bash
sudo /absolute/path/to/extracted-release/deploy/release-stop-the-world.sh \
  /absolute/path/to/extracted-release
```

The controller verifies the release manifest (including migrations and release
tools), copies it into a root-owned stage and verifies it again, takes an
exclusive host lock, isolates nginx, stops `aegis-admin`, `aegis-public`, and
the database-writing `aegis-node`, and proves their PIDs and loopback ports are
gone before it changes the schema. It creates and validates an encrypted
PostgreSQL backup, then
starts the new writers behind the isolated ingress and requires both `healthz`
and dependency-aware `readyz` before restoring traffic.

Before migration starts, failures restore the previous binaries, migrations,
release tools, services, and verified readiness. Once the migration command is
attempted, automatic database downgrade is prohibited: goose may commit several
migrations before a later one fails, so a generic `down-to` can itself leave a
partially downgraded database. The system deliberately remains fail-closed with
ingress and writers stopped; recovery uses the encrypted backup and the reviewed
restore procedure. Once ingress restoration is attempted, the same prohibition
continues because the new schema may already have served real traffic.

Run the repository-side structural gate on Windows builders with:

```powershell
powershell -NoProfile -File deploy/release-stop-the-world_test.ps1
```

On a disposable Linux/root test host, also run the dynamic preflight gate. It
proves that missing arguments, an invalid manifest, and lock contention perform
zero `systemctl stop/start` operations:

```bash
sudo bash deploy/release-stop-the-world_mock_test.sh
sudo bash deploy/check-migrations_mock_test.sh
```
