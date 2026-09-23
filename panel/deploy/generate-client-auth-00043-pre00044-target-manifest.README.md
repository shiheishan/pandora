# CLIENT-AUTH-00043 pre-00044 exact target manifest

This is an independent, read-only catalog exporter candidate. It is not wired
to the signed V3 runner, verifier, migration, release manifest, production
database, or any CIC/drop path. A generated file remains
`CANDIDATE_NOT_RELEASE_APPROVAL`.

The exporter accepts only a locally attested, auto-removed PostgreSQL 18
container on an internal Docker network. The source must be exact Goose 43,
must carry the dedicated disposable-source labels, and must match the supplied
system/database identity plus 00042/00043 meta and protected-verifier proof
hashes. It does not accept a DSN, password, remote Docker context, or arbitrary
`psql` endpoint. The internal network must contain exactly that source
container. Each export transaction rejects every other client backend in the
entire PostgreSQL cluster, regardless of database, takes
`ACCESS SHARE` locks on every target to exclude concurrent DDL, and repeats the
client check before returning. The complete export is then recomputed in a
fresh lock-protected transaction and must be byte-identical before publication;
container identity, network identity, membership and lease are rechecked around
both passes.

The OID-free catalog covers the ten 00044 classification targets and both
existing `app.client_auth_*` meta relations. It records exact relation and
column owner/ACL/RLS/comment state; column type/default/nullability/identity/
generation/collation; constraints; indexes; policies; user triggers; trigger
function identity/source; and normalized dependency identities. Rather than
guessing writable behavior from names or source text, the exporter freezes five
independent catalog products:

- the complete `app` routine catalog, including definition, `prosrc`, ACL and
  both incoming and outgoing catalog dependencies;
- the recursive reverse `pg_depend` closure whose seeds are exactly every
  target relation and every target column; target-trigger functions are
  separately unioned into the routine-closure output and are not seeds;
- the exact target-trigger closure;
- the exact `pg_rewrite`/rule behavior reached by that closure;
- the recursive, cycle-safe, bidirectional `pg_inherits` closure starting from
  target/reverse-dependency relations, including every reached ancestor and
  descendant, every internal edge, and their partition properties.

Each canonical JSON product must equal a separately approved SHA-256 input or
the export returns no row and denies. This is deliberately a catalog dependency
equality contract, not semantic SQL parsing. PostgreSQL does not catalog every
dynamic SQL reference, so the exporter also freezes the complete `app` routine
catalog. Any routine definition/`prosrc`, dependency, rule, inheritance,
partition or trigger drift denies the run. This detects drift from an approved
source; it does not prove what arbitrary dynamic SQL means.

The repository JSON is intentionally `PLACEHOLDER_NO_GO` with a null catalog.
It must never be hand-filled. Only a successful isolated PG18 run may create a
new external candidate.

Publication is Linux-root-only. Every output-directory ancestor is rechecked
as UID 0 and not group/world writable; the stage is a same-device regular
single-link `0600` file. The exporter syncs and re-identifies the stage, uses a
same-directory no-clobber hard link, verifies the linked inode/hash, syncs the
directory, removes the stage link, syncs again, and repeats the ancestor/inode
checks. A normal signal removes the hidden stage. A crash may leave no target,
one complete target, or a complete target plus hidden hard link, but never an
accepted partially written target; absence of the final PASS record remains
fail-closed.

Production does not resolve tools through `PATH` and has no runtime mock/tool
override. The `/usr/bin/bash` interpreter and each fixed `/usr/bin` tool path are
compiled into this script contract; tools and every ancestor must be root-owned
and not group/world writable. Their device/inode/link/size/mtime identity set is
rechecked before and after publication. Hash output is parsed with Bash
`read`; the production path deliberately has no dependency on an
alternatives-managed `awk` executable. The mock creates an instrumented copy
and proves that only the marked fixed-tool block changed; it never self-symlinks
or modifies the production source.

These controls assume the isolated root runner, host kernel, Docker daemon and
fixed tools are trusted. They exclude ordinary network/client/DDL drift and
PATH substitution; they do not claim exclusion of a malicious host root or a
compromised kernel/daemon. Real PostgreSQL 18 and Linux-root execution remain
mandatory release gates.
