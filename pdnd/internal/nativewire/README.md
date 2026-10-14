# Pandora NativeWire boundary

This directory contains the vendored low-level wire implementations used by
the Pandora-owned protocol adapters for Hysteria2, TUIC and AnyTLS.

The adapters in `kernel/` own listener lifecycle, routing, user/device policy,
traffic accounting, shutdown and control-plane integration. The copied wire
packages are deliberately kept behind this `internal/` boundary and are not used
as a general-purpose multi-core runtime.

ShadowTLS v3 is also kept here because its ClientHello HMAC and modified
TLS-record state machine must be version-pinned and audited before public
release.

The Hysteria2 and TUIC forks add synchronized user-map publication. The AnyTLS
fork adds the same read/write protection for password lookup. This prevents a
hot user update from racing with an authentication read in the low-level
service. Source licenses are retained next to each fork.

The fork is not a claim that all protocol cryptography was reimplemented from
scratch. The release checklist must still track upstream version, license,
interoperability, and Linux race evidence for each wire package. ShadowTLS is
validated through Pandora's adapter and a real v3 loopback with a decoy TLS
server; strict TLS 1.3-only mode remains an explicit compatibility option.

`mkcp/`, `udpmask/` and `dgram/` are different from the forks above.

`dgram/` is a small Pandora helper shared by the Hysteria2 and TUIC forks: it
asks quic-go for the connection's real DATAGRAM limit (minus room for an ACK
frame in the same packet) so UDP messages are fragmented only when they really
do not fit, instead of at the upstream fixed 1197 bytes.

They carry no upstream LICENSE file and their source does not claim to be copied
from any upstream file: they are in-repository implementations written against
the Xray-core wire formats — mKCP segments (`mkcp/`) and the finalmask UDP masks
such as `aes128gcm` (`udpmask/`) — with the deliberate deviations from Xray
documented in their comments (for example the congestion window in
`mkcp/congestion.go`). `kernel/mkcp_transport.go` is their only consumer.

Byte-level and connection-level compatibility with Xray is checked by the
`-tags interop` tests `mkcp/segment_xray_test.go`, `mkcp/interop_xray_test.go`
and `udpmask/interop_xray_test.go`; "Source licenses are retained next to each
fork" above applies only to the four forked packages (anytls, hysteria2,
shadowtls, tuic).
