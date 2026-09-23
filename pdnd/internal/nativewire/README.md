# Pandora NativeWire boundary

This directory contains the vendored low-level wire implementations used by
the Pandora-owned protocol adapters for Hysteria2, TUIC and AnyTLS.

The adapters in `kernel/` own listener lifecycle, routing, user/device policy,
traffic accounting, shutdown and control-plane integration. The copied wire
packages are deliberately kept behind this `internal/` boundary and are not
used as a general-purpose multi-core runtime. ShadowTLS v3 is also kept here
because its ClientHello HMAC and modified TLS-record state machine must be
version-pinned and audited before public release.

The Hysteria2 and TUIC forks add synchronized user-map publication. The AnyTLS
fork adds the same read/write protection for password lookup. This prevents a
hot user update from racing with an authentication read in the low-level
service. Source licenses are retained next to each fork.

The fork is not a claim that all protocol cryptography was reimplemented from
scratch. The release checklist must still track upstream version, license,
interoperability, and Linux race evidence for each wire package. ShadowTLS is
validated through Pandora's adapter and a real v3 loopback with a decoy TLS
server; strict TLS 1.3-only mode remains an explicit compatibility option.
