# CA42 V3 Admission contract

This package freezes the recovery decision table before any production
mutation entry point is enabled. It is not an admission implementation and its
public diagnostic values are not authority.

The only legal durable success order is:

1. nonce `RESERVED`;
2. Journal `ADMISSION_RESERVED`;
3. Journal `ADMISSION_ATTEMPTED`;
4. one opaque, idempotent consumer operation;
5. Journal `ADMISSION_VERIFIED`;
6. nonce `COMMITTED`.

Every step must be derived from and revalidated against the live opaque
`ConsumptionIdentity`, Journal boundary, inventory, committed authority-ledger
lease, nonce reservation and consumer receipt. No production API may accept a
caller-created snapshot, hash, timestamp, size, operation ID or scalar input
structure as authority.

Unknown consumer outcome is queried, never blindly retried. Binding divergence,
expired claims before the consumer attempt, or ambiguous durable publication
move monotonically to `RECOVERY_REQUIRED`.

Admission remains disabled until the retained authority-ledger capability,
opaque inventory input, nonce mutation adapter, Journal admission publisher and
queryable idempotent consumer capability are connected by the sole coordinator.
