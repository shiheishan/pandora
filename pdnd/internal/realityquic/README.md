# Pandora Native QUIC/HTTP3 fork

This directory is a repository-owned fork of `github.com/apernet/quic-go`
used by Pandora NativeCore's REALITY-over-QUIC boundary. The transport code
is kept local so its TLS event wiring can bind to
`internal/reality` instead of the standard `crypto/tls` runtime.

The fork intentionally excludes upstream tests, examples, fuzzing and
integration fixtures from the production tree. Keep the upstream `LICENSE`
with this copy and re-run the focused package tests plus the
`kernel/TestNativeRealityH3RoundTrip` acceptance test after upgrades.
