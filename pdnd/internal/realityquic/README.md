# Pandora Native QUIC/HTTP3 fork

This directory is a repository-owned fork of `github.com/apernet/quic-go`
used by Pandora NativeCore's REALITY-over-QUIC boundary. The transport code
is kept local so its TLS event wiring can bind to
`internal/reality` instead of the standard `crypto/tls` runtime.

The fork intentionally excludes upstream tests, examples, fuzzing and
integration fixtures from the production tree. Keep the upstream `LICENSE`
with this copy and re-run the `kernel/TestNativeRealityH3RoundTrip`
acceptance test after upgrades; this directory itself contains no tests.

When syncing from upstream, rewrite only Go import paths and `go:generate`
package paths to `github.com/aegispanel/nodeagent/internal/realityquic`.
Web links (`issues/`, `pull/`, `wiki/`) must keep pointing at
`https://github.com/quic-go/quic-go/`: the issue and PR numbers belong to
that project, and apernet's tree already ships them rewritten to its own
fork, where they do not resolve.
