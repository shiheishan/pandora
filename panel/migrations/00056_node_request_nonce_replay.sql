-- +goose Up

-- PostgreSQL is the authoritative replay ledger for signed node requests.
-- This keeps nonce claims consistent across API processes and restarts.
-- +goose StatementBegin
CREATE TABLE node_request_nonces (
  tenant_id           uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  node_id             uuid        NOT NULL,
  nonce               bytea       NOT NULL CHECK (octet_length(nonce) = 16),
  request_fingerprint bytea       NOT NULL CHECK (octet_length(request_fingerprint) = 32),
  request_ts          timestamptz NOT NULL,
  expires_at          timestamptz NOT NULL,
  claimed_at          timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, node_id, nonce),
  FOREIGN KEY (tenant_id, node_id)
    REFERENCES nodes (tenant_id, id) ON DELETE CASCADE,
  CHECK (expires_at > request_ts)
);

CREATE INDEX idx_node_request_nonces_expiry
  ON node_request_nonces (expires_at);

SELECT app.enable_tenant_rls('node_request_nonces');

-- The application only inserts claims and performs bounded expiry deletion.
-- Consumed claims are immutable even if application code is compromised.
GRANT SELECT, INSERT, DELETE ON node_request_nonces TO aegis_app;
REVOKE UPDATE, TRUNCATE ON node_request_nonces FROM aegis_app;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS node_request_nonces;
