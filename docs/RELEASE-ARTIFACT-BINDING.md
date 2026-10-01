# Node release-artifact binding

First-time node enrollment is fail-closed in production unless the control
plane is configured with the exact native-node release that the installer is
allowed to activate. The values are deployment configuration, not user input
and must not be derived from request headers or bootstrap commands.

The node gateway (`aegis-node`, which serves the enrollment commit) reads:

```text
PANDORA_NATIVE_RELEASE_VERSION=<pandora-native buildVersion: the release version>
PANDORA_NATIVE_ARTIFACT_AMD64_SHA256=<sha256 of pdnd-dist/pandora-native-linux-amd64>
PANDORA_NATIVE_ARTIFACT_ARM64_SHA256=<sha256 of pdnd-dist/pandora-native-linux-arm64>
```

and enforces them only when `AEGIS_ENV=production`. The panel compares the
submitted `binary_sha256`, `architecture`, and `agent_version` evidence with
these values inside the commit transaction. A missing production binding or
any mismatch rejects the commit; development and disposable test environments
may omit the binding.

## From build to enforcement

A release bundle carries the binding and installs it without any hand-copied
hash.

1. **Generate.** `panel/deploy/build-release.sh` builds both
   `pandora-native-linux-{amd64,arm64}` into the bundle's `pdnd-dist/` with
   `-X main.buildVersion=$VERSION`, where `$VERSION` is `PANDORA_VERSION` (or
   `git describe`).

   It then writes `deploy/release-artifact.env` with
   `PANDORA_NATIVE_RELEASE_VERSION=$VERSION` and the SHA-256 of the two files it
   just packaged. The file is listed in the bundle's `SHA256SUMS`, so the
   out-of-band manifest digest covers it. It deliberately does not set
   `AEGIS_ENV`: the run mode belongs to the host's `.env`.
2. **Install.** `install.sh` refuses a bundle without the file, then runs
   `install-linux-binaries.sh`, which verifies `SHA256SUMS` against the
   out-of-band digest and installs `release-artifact.env` to
   `/opt/aegispanel/deploy/release-artifact.env` in the same transaction as the
   binaries (rolled back together on failure).

   `release-stop-the-world.sh` re-checks the two digests against the bundle's
   `pdnd-dist/` and places the file at the same path during a schema-changing
   release; it does not install systemd units, so a host must have received the
   current `aegis-node.service` through `install.sh` at least once.

   `install-native.sh` copies it next to its `.env` under `/opt/pandora`.
3. **Load.** `deploy/systemd/aegis-node.service` loads
   `EnvironmentFile=/opt/aegispanel/deploy/.env` and then
   `EnvironmentFile=-/opt/aegispanel/deploy/release-artifact.env`. The bundle's
   file comes second so its values win over any stale hand-written copy in
   `.env`. The leading `-` keeps the gateway starting if the file is missing;
   enrollment then still fails closed in production.
4. **Upgrade.** Every bundle install overwrites the file with the new
   release's values and the gateways are restarted, so the binding always
   names the `pdnd-dist/` binaries the panel is serving.

## Run mode

A bundle install is a production install. On a first install, `install.sh`
writes `AEGIS_ENV=production` into the generated `.env`. Production also makes
every gateway require `AEGIS_PUBLIC_BASE_URL` to be an `https://` URL on a
public host (`platform/config` `CanonicalPublicOrigin`), so the first install
takes the domain from `PANDORA_PUBLIC_BASE_URL` or an interactive prompt and
stops before changing anything if neither gives a valid `https://<domain>`.

An upgrade never changes the run mode of an existing `.env`. If it is not
`production`, `install.sh` prints a warning with the steps to switch, but
leaves the decision to the operator. The template `deploy/.env.example` keeps
`AEGIS_ENV=development` for local development.

## Other deployments

A panel not installed from a bundle must set the three variables in the
`aegis-node` service environment itself, taking the digests from the
`SHA256SUMS` of the bundle whose `pdnd-dist/` it serves, after verifying that
file independently. Do not mix values from different release archives.

The installer also submits canonical SHA-256 evidence for the installed
config, non-root systemd unit, and `validate-install` preflight output. Those
values are persisted as the immutable `commit_evidence` JSON for audit and
cannot be changed after a committed enrollment.

The schema-changing maintenance controller has a separate supply-chain gate:
invoke it with `PANDORA_RELEASE_MANIFEST_SHA256` set from the out-of-band
`<archive>.manifest.sha256` sidecar. It rejects missing or mismatched manifests
and refuses source trees or parents that are not root-owned and non-writable.
