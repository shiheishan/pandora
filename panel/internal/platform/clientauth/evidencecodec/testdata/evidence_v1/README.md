# CLIENT-AUTH-00044 evidence codec vectors

`logical_vectors.json` is the semantic input. `expected_vectors.json` is
accepted only when the Go encoder and the dependency-free PowerShell oracle
independently produce the same bytes and digests.

The vectors cover the normative empty row-set plus a non-empty row containing
UUID, NULL, negative PostgreSQL-epoch timestamp, finite numeric, recursive
JSONB, padded `bpchar(2)`, IPv4/IPv6 host bits, an `ndim=0` empty array, and a
non-1-lower-bound array with a NULL element.

These are codec conformance vectors, not PostgreSQL 18 integration evidence and
not release approval.
