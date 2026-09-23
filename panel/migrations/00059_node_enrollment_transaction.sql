-- +goose Up
-- +goose StatementBegin
-- Enrollment is deliberately separate from node_identities: a pending
-- candidate must never authenticate to the normal node or UniProxy APIs.
ALTER TABLE bootstrap_tokens
  ADD CONSTRAINT bootstrap_tokens_tenant_id_id_unique UNIQUE (tenant_id, id);

CREATE TABLE node_enrollments (
  id                    uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id             uuid NOT NULL,
  node_id               uuid NOT NULL,
  bootstrap_token_id    uuid NOT NULL,
  use_ordinal           smallint NOT NULL CHECK (use_ordinal > 0),
  request_id            uuid NOT NULL,
  begin_request_sha256  bytea NOT NULL CHECK (octet_length(begin_request_sha256) = 32),
  candidate_serial      integer NOT NULL CHECK (candidate_serial > 0),
  public_key            bytea NOT NULL CHECK (octet_length(public_key) = 32),
  fingerprint           bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
  runtime_token_hash    bytea NOT NULL CHECK (octet_length(runtime_token_hash) = 32),
  config_signing_key_id text NOT NULL CHECK (config_signing_key_id ~ '^[A-Za-z0-9_-]{11}$'),
  config_signing_public_key bytea NOT NULL CHECK (octet_length(config_signing_public_key) = 32),
  state                 text NOT NULL DEFAULT 'pending'
                          CHECK (state IN ('pending','committed','aborted','expired')),
  commit_request_sha256 bytea CHECK (commit_request_sha256 IS NULL OR octet_length(commit_request_sha256) = 32),
  commit_evidence       jsonb,
  expires_at            timestamptz NOT NULL,
  committed_at          timestamptz,
  aborted_at            timestamptz,
  abort_reason          text,
  created_at            timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT node_enrollments_tenant_id_id_unique UNIQUE (tenant_id, id),
  CONSTRAINT node_enrollments_request_unique UNIQUE (tenant_id, request_id),
  CONSTRAINT node_enrollments_token_use_unique UNIQUE (bootstrap_token_id, use_ordinal),
  CONSTRAINT node_enrollments_serial_unique UNIQUE (node_id, candidate_serial),
  CONSTRAINT node_enrollments_fingerprint_unique UNIQUE (fingerprint),
  CONSTRAINT node_enrollments_node_fk FOREIGN KEY (tenant_id, node_id)
    REFERENCES nodes (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT node_enrollments_token_fk FOREIGN KEY (tenant_id, bootstrap_token_id)
    REFERENCES bootstrap_tokens (tenant_id, id)
    ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT node_enrollments_expiry_check CHECK (expires_at > created_at),
  CONSTRAINT node_enrollments_state_shape_check CHECK (
    (state = 'pending' AND committed_at IS NULL AND aborted_at IS NULL AND commit_request_sha256 IS NULL AND commit_evidence IS NULL)
    OR (state = 'committed' AND committed_at IS NOT NULL AND aborted_at IS NULL
        AND commit_request_sha256 IS NOT NULL AND commit_evidence IS NOT NULL AND abort_reason IS NULL
        AND committed_at >= created_at AND committed_at <= expires_at)
    OR (state IN ('aborted','expired') AND committed_at IS NULL AND aborted_at IS NOT NULL
        AND commit_request_sha256 IS NULL AND commit_evidence IS NULL)
  )
);

CREATE UNIQUE INDEX uq_node_enrollments_pending_node
  ON node_enrollments (tenant_id, node_id) WHERE state = 'pending';
CREATE INDEX idx_node_enrollments_pending_expiry
  ON node_enrollments (expires_at) WHERE state = 'pending';

CREATE OR REPLACE FUNCTION app.guard_node_enrollment() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.state <> 'pending' THEN
    RAISE EXCEPTION 'terminal node enrollment is immutable';
  END IF;
  IF NEW.state NOT IN ('committed','aborted','expired') THEN
    RAISE EXCEPTION 'invalid node enrollment transition';
  END IF;
  IF ROW(NEW.id,NEW.tenant_id,NEW.node_id,NEW.bootstrap_token_id,NEW.use_ordinal,
         NEW.request_id,NEW.begin_request_sha256,NEW.candidate_serial,
         NEW.public_key,NEW.fingerprint,NEW.runtime_token_hash,
    NEW.config_signing_key_id,NEW.config_signing_public_key,NEW.expires_at,NEW.created_at)
     IS DISTINCT FROM
     ROW(OLD.id,OLD.tenant_id,OLD.node_id,OLD.bootstrap_token_id,OLD.use_ordinal,
         OLD.request_id,OLD.begin_request_sha256,OLD.candidate_serial,
         OLD.public_key,OLD.fingerprint,OLD.runtime_token_hash,
         OLD.config_signing_key_id,OLD.config_signing_public_key,OLD.expires_at,OLD.created_at) THEN
    RAISE EXCEPTION 'node enrollment identity fields are immutable';
  END IF;
  IF NEW.state = 'committed' THEN
    IF clock_timestamp() >= OLD.expires_at THEN
      RAISE EXCEPTION 'expired node enrollment cannot be committed';
    END IF;
    NEW.committed_at := clock_timestamp();
    NEW.aborted_at := NULL;
    NEW.abort_reason := NULL;
    IF NOT EXISTS (
      SELECT 1 FROM node_identities i
       WHERE i.tenant_id=NEW.tenant_id AND i.node_id=NEW.node_id
         AND i.serial=NEW.candidate_serial AND i.public_key=NEW.public_key
         AND i.fingerprint=NEW.fingerprint AND i.status='active'
         AND i.expires_at > clock_timestamp()
    ) OR NOT EXISTS (
      SELECT 1 FROM nodes n
       WHERE n.tenant_id=NEW.tenant_id AND n.id=NEW.node_id
         AND n.server_token_hash=NEW.runtime_token_hash
    ) THEN
      RAISE EXCEPTION 'node enrollment cannot commit without matching active credentials';
    END IF;
  ELSE
    NEW.committed_at := NULL;
    NEW.commit_request_sha256 := NULL;
    NEW.commit_evidence := NULL;
    NEW.aborted_at := clock_timestamp();
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER trg_node_enrollments_transition
  BEFORE UPDATE ON node_enrollments
  FOR EACH ROW EXECUTE FUNCTION app.guard_node_enrollment();

SELECT app.enable_tenant_rls('node_enrollments');
GRANT SELECT, INSERT ON node_enrollments TO aegis_app;
REVOKE UPDATE, DELETE, TRUNCATE ON node_enrollments FROM aegis_app;
GRANT UPDATE (state, commit_request_sha256, commit_evidence, abort_reason)
  ON node_enrollments TO aegis_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
  can_bypass boolean;
BEGIN
  SELECT rolsuper OR rolbypassrls INTO can_bypass
    FROM pg_roles WHERE rolname = current_user;
  IF NOT coalesce(can_bypass, false) THEN
    RAISE EXCEPTION 'node enrollment rollback requires a BYPASSRLS migration role';
  END IF;
END
$$;

SET LOCAL row_security = off;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM node_enrollments) THEN
    RAISE EXCEPTION 'refusing to remove node enrollment evidence';
  END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_node_enrollments_transition ON node_enrollments;
DROP FUNCTION IF EXISTS app.guard_node_enrollment();
DROP TABLE node_enrollments;
ALTER TABLE bootstrap_tokens
  DROP CONSTRAINT bootstrap_tokens_tenant_id_id_unique;
-- +goose StatementEnd
