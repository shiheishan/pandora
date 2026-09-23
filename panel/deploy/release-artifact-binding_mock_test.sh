#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD="$ROOT/deploy/build-release.sh"

bash -n "$BUILD"
for fragment in \
  'PANDORA_NATIVE_ARTIFACT_AMD64_SHA256' \
  'PANDORA_NATIVE_ARTIFACT_ARM64_SHA256' \
  'PANDORA_NATIVE_RELEASE_VERSION' \
  'release-artifact.env' \
  'sha256sum "$target/pdnd-dist/pandora-native-linux-amd64"' \
  'sha256sum "$target/pdnd-dist/pandora-native-linux-arm64"' \
  '"$target_base/deploy/release-artifact.env"'; do
  grep -Fq "$fragment" "$BUILD" || {
    echo "release builder is missing artifact binding fragment: $fragment" >&2
    exit 1
  }
done
printf 'release-artifact-binding mock: PASS\n'
