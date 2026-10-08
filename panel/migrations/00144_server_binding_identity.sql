-- 服务器级绑定 P1：服务器身份、服务器接入、绑定令牌（用户 2026-10-07 定「一台机器装一次 pdnd、
-- 绑定面板里的服务器、可绑多个面板」；合约 docs/server-binding-contract.md §3、§12、§18；w9bind）。
--
-- 和按节点接入（node_identities / node_enrollments）并存，互不替代：
--   - bootstrap_tokens 加 kind（node | server）。kind=server 的是绑定令牌：绑定一台服务器、
--     只能用一次、不挂节点与池；哈希域与节点令牌不同，两条接入互相拿不到对方的令牌。
--   - server_enrollments：服务器接入 S1 的 begin → commit / abort 证据链，结构照 node_enrollments。
--     pending 的候选公钥只能用来签本次接入的 status / commit / abort，不能当服务器身份用。
--   - server_identities：commit 之后的服务器身份（Ed25519 签名公钥 + X25519 加密公钥）。
--     一台服务器同时只有一个 active 身份（唯一部分索引）：同一面板第二台机器拿同一台服务器
--     commit 会撞上它被拒。吊销只能 active → revoked，理由取合约墓碑的四个取值。
--   - server_request_nonces：服务器签名请求 nonce 的 PG 回落账，口径同节点那张：
--     主路径在 Valkey，Valkey 出错时在这里认领，重放撞主键。
--
-- 读写它们的 Go：nodefabric 的 server_identity*.go、server_enrollment*.go；后台绑定令牌、
-- 绑定状态与解除绑定；节点网关的 /v1/servers/enrollments*。
--
-- 运行角色在生产会被 configure-app-role.sql 的整表重授放宽，所以不变量放在触发器里：
-- 接入终态不可改、身份的密钥材料不可改、吊销不可逆。
--
-- 锁：bootstrap_tokens 加列带常量默认值不重写表，加 CHECK 要扫一遍（表很小，毫秒级）；
-- 其余是新建的空表。lock_timeout 拿不到锁就失败重来，不排在业务事务后面。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
ALTER TABLE bootstrap_tokens
  ADD COLUMN kind text NOT NULL DEFAULT 'node';

ALTER TABLE bootstrap_tokens
  ADD CONSTRAINT bootstrap_tokens_kind_check CHECK (kind IN ('node', 'server'));

-- 绑定令牌的形状：必须绑一台服务器、只能用一次、不挂节点 / 池 / 模板。
-- 节点接入路径会往令牌上回写 node_id，撞上这条就失败，不会把绑定令牌当节点令牌用掉。
ALTER TABLE bootstrap_tokens
  ADD CONSTRAINT bootstrap_tokens_server_kind_shape CHECK (
    kind <> 'server' OR (server_id IS NOT NULL AND node_id IS NULL AND pool_id IS NULL
                         AND template_id IS NULL AND max_uses = 1));

COMMENT ON COLUMN bootstrap_tokens.kind IS
  'node：节点接入令牌；server：服务器绑定令牌（绑定一台服务器、单次、1 小时）。';

CREATE TABLE server_enrollments (
  id                        uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id                 uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  server_id                 uuid NOT NULL,
  bootstrap_token_id        uuid NOT NULL,
  request_id                uuid NOT NULL,
  begin_request_sha256      bytea NOT NULL CHECK (octet_length(begin_request_sha256) = 32),
  candidate_serial          integer NOT NULL CHECK (candidate_serial > 0),
  public_key                bytea NOT NULL CHECK (octet_length(public_key) = 32),
  fingerprint               bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
  enc_kem                   text NOT NULL CHECK (enc_kem = 'dhkem-x25519-hkdf-sha256'),
  enc_public_key            bytea NOT NULL CHECK (octet_length(enc_public_key) = 32),
  agent_version             text NOT NULL
                              CHECK (agent_version ~ '^v(0|[1-9][0-9]{0,9})\.(0|[1-9][0-9]{0,9})\.(0|[1-9][0-9]{0,9})$'),
  features                  text[] NOT NULL DEFAULT '{}',
  capabilities              jsonb CHECK (capabilities IS NULL OR jsonb_typeof(capabilities) = 'object'),
  hostname                  text CHECK (hostname IS NULL OR char_length(hostname) BETWEEN 1 AND 253),
  cpu_cores                 int CHECK (cpu_cores IS NULL OR cpu_cores > 0),
  memory_mb                 int CHECK (memory_mb IS NULL OR memory_mb > 0),
  disk_gb                   int CHECK (disk_gb IS NULL OR disk_gb > 0),
  config_signing_key_id     text NOT NULL CHECK (config_signing_key_id ~ '^[A-Za-z0-9_-]{11}$'),
  config_signing_public_key bytea NOT NULL CHECK (octet_length(config_signing_public_key) = 32),
  state                     text NOT NULL DEFAULT 'pending'
                              CHECK (state IN ('pending', 'committed', 'aborted', 'expired')),
  commit_request_sha256     bytea CHECK (commit_request_sha256 IS NULL OR octet_length(commit_request_sha256) = 32),
  commit_evidence           jsonb,
  expires_at                timestamptz NOT NULL,
  committed_at              timestamptz,
  aborted_at                timestamptz,
  abort_reason              text CHECK (abort_reason IS NULL OR char_length(abort_reason) <= 500),
  created_at                timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT server_enrollments_tenant_id_id_unique UNIQUE (tenant_id, id),
  CONSTRAINT server_enrollments_request_unique UNIQUE (tenant_id, request_id),
  -- 绑定令牌只能用一次：一张令牌最多对应一次接入
  CONSTRAINT server_enrollments_token_unique UNIQUE (bootstrap_token_id),
  CONSTRAINT server_enrollments_serial_unique UNIQUE (tenant_id, server_id, candidate_serial),
  CONSTRAINT server_enrollments_fingerprint_unique UNIQUE (fingerprint),
  CONSTRAINT server_enrollments_server_fk FOREIGN KEY (tenant_id, server_id)
    REFERENCES servers (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT server_enrollments_token_fk FOREIGN KEY (tenant_id, bootstrap_token_id)
    REFERENCES bootstrap_tokens (tenant_id, id)
    ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT server_enrollments_expiry_check CHECK (expires_at > created_at),
  CONSTRAINT server_enrollments_state_shape_check CHECK (
    (state = 'pending' AND committed_at IS NULL AND aborted_at IS NULL
        AND commit_request_sha256 IS NULL AND commit_evidence IS NULL)
    OR (state = 'committed' AND committed_at IS NOT NULL AND aborted_at IS NULL
        AND commit_request_sha256 IS NOT NULL AND commit_evidence IS NOT NULL AND abort_reason IS NULL
        AND committed_at >= created_at AND committed_at <= expires_at)
    OR (state IN ('aborted', 'expired') AND committed_at IS NULL AND aborted_at IS NOT NULL
        AND commit_request_sha256 IS NULL AND commit_evidence IS NULL)
  )
);

-- 一台服务器同时只有一个进行中的接入
CREATE UNIQUE INDEX uq_server_enrollments_pending_server
  ON server_enrollments (tenant_id, server_id) WHERE state = 'pending';
-- 取下一个候选序号、绑定状态里的「进行中」都按服务器查
CREATE INDEX idx_server_enrollments_server
  ON server_enrollments (tenant_id, server_id, created_at DESC);

CREATE TABLE server_identities (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  server_id      uuid NOT NULL,
  -- 每次重新绑定递增；验签按（租户, 服务器, serial）取 active 的那一份
  serial         integer NOT NULL CHECK (serial > 0),
  public_key     bytea NOT NULL CHECK (octet_length(public_key) = 32),
  fingerprint    bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
  enc_kem        text NOT NULL CHECK (enc_kem = 'dhkem-x25519-hkdf-sha256'),
  enc_public_key bytea NOT NULL CHECK (octet_length(enc_public_key) = 32),
  agent_version  text NOT NULL
                   CHECK (agent_version ~ '^v(0|[1-9][0-9]{0,9})\.(0|[1-9][0-9]{0,9})\.(0|[1-9][0-9]{0,9})$'),
  features       text[] NOT NULL DEFAULT '{}',
  capabilities   jsonb CHECK (capabilities IS NULL OR jsonb_typeof(capabilities) = 'object'),
  enrollment_id  uuid NOT NULL,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  -- 取合约清单墓碑 unbound_reason 的取值，P2 原样下发
  revoked_reason text CHECK (revoked_reason IN
                   ('server_deleted', 'server_unbound', 'identity_revoked', 'machine_unbound')),
  revoked_at     timestamptz,
  issued_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT server_identities_tenant_id_id_unique UNIQUE (tenant_id, id),
  CONSTRAINT server_identities_server_serial_unique UNIQUE (tenant_id, server_id, serial),
  CONSTRAINT server_identities_fingerprint_unique UNIQUE (fingerprint),
  CONSTRAINT server_identities_enrollment_unique UNIQUE (tenant_id, enrollment_id),
  CONSTRAINT server_identities_server_fk FOREIGN KEY (tenant_id, server_id)
    REFERENCES servers (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT server_identities_enrollment_fk FOREIGN KEY (tenant_id, enrollment_id)
    REFERENCES server_enrollments (tenant_id, id),
  CONSTRAINT server_identities_status_shape_check CHECK (
    (status = 'active' AND revoked_at IS NULL AND revoked_reason IS NULL)
    OR (status = 'revoked' AND revoked_at IS NOT NULL AND revoked_reason IS NOT NULL)
  )
);

-- 一台服务器同时只有一个有效身份：同一面板（同租户）第二台机器绑同一台服务器在这里被拒
CREATE UNIQUE INDEX uq_server_identities_active
  ON server_identities (tenant_id, server_id) WHERE status = 'active';

CREATE TABLE server_request_nonces (
  tenant_id           uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
  server_id           uuid        NOT NULL,
  nonce               bytea       NOT NULL CHECK (octet_length(nonce) = 16),
  request_fingerprint bytea       NOT NULL CHECK (octet_length(request_fingerprint) = 32),
  request_ts          timestamptz NOT NULL,
  expires_at          timestamptz NOT NULL,
  claimed_at          timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, server_id, nonce),
  FOREIGN KEY (tenant_id, server_id)
    REFERENCES servers (tenant_id, id) ON DELETE CASCADE,
  CHECK (expires_at > request_ts)
);

CREATE INDEX idx_server_request_nonces_expiry
  ON server_request_nonces (expires_at);

-- 接入状态机：终态不可改；身份字段不可改；commit 必须已有与候选完全一致的 active 身份
CREATE FUNCTION app.guard_server_enrollment() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.state <> 'pending' THEN
    RAISE EXCEPTION 'terminal server enrollment is immutable';
  END IF;
  IF NEW.state NOT IN ('committed', 'aborted', 'expired') THEN
    RAISE EXCEPTION 'invalid server enrollment transition';
  END IF;
  IF ROW(NEW.id, NEW.tenant_id, NEW.server_id, NEW.bootstrap_token_id, NEW.request_id,
         NEW.begin_request_sha256, NEW.candidate_serial, NEW.public_key, NEW.fingerprint,
         NEW.enc_kem, NEW.enc_public_key, NEW.agent_version, NEW.features, NEW.capabilities,
         NEW.hostname, NEW.cpu_cores, NEW.memory_mb, NEW.disk_gb,
         NEW.config_signing_key_id, NEW.config_signing_public_key, NEW.expires_at, NEW.created_at)
     IS DISTINCT FROM
     ROW(OLD.id, OLD.tenant_id, OLD.server_id, OLD.bootstrap_token_id, OLD.request_id,
         OLD.begin_request_sha256, OLD.candidate_serial, OLD.public_key, OLD.fingerprint,
         OLD.enc_kem, OLD.enc_public_key, OLD.agent_version, OLD.features, OLD.capabilities,
         OLD.hostname, OLD.cpu_cores, OLD.memory_mb, OLD.disk_gb,
         OLD.config_signing_key_id, OLD.config_signing_public_key, OLD.expires_at, OLD.created_at) THEN
    RAISE EXCEPTION 'server enrollment identity fields are immutable';
  END IF;
  IF NEW.state = 'committed' THEN
    IF clock_timestamp() >= OLD.expires_at THEN
      RAISE EXCEPTION 'expired server enrollment cannot be committed';
    END IF;
    NEW.committed_at := clock_timestamp();
    NEW.aborted_at := NULL;
    NEW.abort_reason := NULL;
    IF NOT EXISTS (
      SELECT 1 FROM server_identities i
       WHERE i.tenant_id = NEW.tenant_id AND i.server_id = NEW.server_id
         AND i.enrollment_id = NEW.id AND i.serial = NEW.candidate_serial
         AND i.public_key = NEW.public_key AND i.fingerprint = NEW.fingerprint
         AND i.enc_public_key = NEW.enc_public_key AND i.status = 'active'
    ) THEN
      RAISE EXCEPTION 'server enrollment cannot commit without a matching active identity';
    END IF;
  ELSE
    NEW.committed_at := NULL;
    NEW.commit_request_sha256 := NULL;
    NEW.commit_evidence := NULL;
    NEW.aborted_at := clock_timestamp();
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER trg_server_enrollments_transition
  BEFORE UPDATE ON server_enrollments
  FOR EACH ROW EXECUTE FUNCTION app.guard_server_enrollment();

-- 身份：密钥材料与归属不可改；吊销只能 active → revoked 且不可逆；
-- active 期间只允许更新上报类元数据（agent 版本、特性、能力表，P2 的合并上报会写）
CREATE FUNCTION app.guard_server_identity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status <> 'active' THEN
    RAISE EXCEPTION 'revoked server identity is immutable';
  END IF;
  IF ROW(NEW.id, NEW.tenant_id, NEW.server_id, NEW.serial, NEW.public_key, NEW.fingerprint,
         NEW.enc_kem, NEW.enc_public_key, NEW.enrollment_id, NEW.issued_at)
     IS DISTINCT FROM
     ROW(OLD.id, OLD.tenant_id, OLD.server_id, OLD.serial, OLD.public_key, OLD.fingerprint,
         OLD.enc_kem, OLD.enc_public_key, OLD.enrollment_id, OLD.issued_at) THEN
    RAISE EXCEPTION 'server identity key material is immutable';
  END IF;
  IF NEW.status = 'revoked' THEN
    NEW.revoked_at := clock_timestamp();
  ELSIF NEW.revoked_at IS NOT NULL OR NEW.revoked_reason IS NOT NULL THEN
    RAISE EXCEPTION 'active server identity cannot carry revocation fields';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER trg_server_identities_guard
  BEFORE UPDATE ON server_identities
  FOR EACH ROW EXECUTE FUNCTION app.guard_server_identity();

SELECT app.enable_tenant_rls('server_enrollments');
SELECT app.enable_tenant_rls('server_identities');
SELECT app.enable_tenant_rls('server_request_nonces');

GRANT SELECT, INSERT ON server_enrollments TO aegis_app;
REVOKE UPDATE, DELETE, TRUNCATE ON server_enrollments FROM aegis_app;
GRANT UPDATE (state, commit_request_sha256, commit_evidence, abort_reason)
  ON server_enrollments TO aegis_app;

GRANT SELECT, INSERT ON server_identities TO aegis_app;
REVOKE UPDATE, DELETE, TRUNCATE ON server_identities FROM aegis_app;
GRANT UPDATE (status, revoked_reason, revoked_at, agent_version, features, capabilities)
  ON server_identities TO aegis_app;

-- 应用只认领与按到期清理；认领过的行不可改
GRANT SELECT, INSERT, DELETE ON server_request_nonces TO aegis_app;
REVOKE UPDATE, TRUNCATE ON server_request_nonces FROM aegis_app;

COMMENT ON TABLE server_enrollments IS
  '服务器接入 S1（begin → commit / abort）的证据链；pending 的候选公钥只能签本次接入的请求。';
COMMENT ON TABLE server_identities IS
  '服务器级绑定的服务器身份；一台服务器同时只有一个 active，吊销不可逆，serial 单调不复用。';
COMMENT ON TABLE server_request_nonces IS
  '服务器签名请求 nonce 的 PG 回落账（主路径在 Valkey），重放撞主键。';
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
-- 数据守卫：已有服务器接入或身份时拒绝回退，它们是谁在什么时候绑定过这台服务器的证据。
-- 守卫要看见全部租户的行，所以要求能绕过 RLS 的迁移角色（照 00059）。
DO $$
DECLARE
  can_bypass boolean;
BEGIN
  SELECT rolsuper OR rolbypassrls INTO can_bypass
    FROM pg_roles WHERE rolname = current_user;
  IF NOT coalesce(can_bypass, false) THEN
    RAISE EXCEPTION 'server binding rollback requires a BYPASSRLS migration role';
  END IF;
END
$$;

SET LOCAL row_security = off;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM server_identities) OR EXISTS (SELECT 1 FROM server_enrollments) THEN
    RAISE EXCEPTION 'refusing to remove server binding evidence (server identities or enrollments exist)';
  END IF;
END
$$;

-- 没用过的绑定令牌只是一次性凭据，旧版本既认不出也用不了，回退时作废（删除）
DELETE FROM bootstrap_tokens WHERE kind = 'server';

DROP TABLE server_request_nonces;
DROP TRIGGER trg_server_identities_guard ON server_identities;
DROP TABLE server_identities;
DROP TRIGGER trg_server_enrollments_transition ON server_enrollments;
DROP TABLE server_enrollments;
DROP FUNCTION app.guard_server_identity();
DROP FUNCTION app.guard_server_enrollment();

ALTER TABLE bootstrap_tokens DROP CONSTRAINT bootstrap_tokens_server_kind_shape;
ALTER TABLE bootstrap_tokens DROP CONSTRAINT bootstrap_tokens_kind_check;
ALTER TABLE bootstrap_tokens DROP COLUMN kind;
-- +goose StatementEnd
