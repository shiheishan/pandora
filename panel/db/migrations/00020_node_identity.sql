-- 节点通道的身份：面板自带的 CA、每台服务器的客户端证书身份、一次性接入令牌。属主 node
-- （docs/architecture.md 第 11.1 节；节点通道契约见 p5 的说明）。
--
--   - node_ca：单行（主键钉死为 1）。私钥经 crypto.Keys.Sealer() 封存后存库，明文私钥不落库。
--     CA 不改不删：换 CA 等于全部服务器重新接入，要另写流程，不靠 UPDATE。
--   - server_identities：一台服务器至多一份有效身份（active）与一份待生效身份（pending），各一条部分唯一索引。
--     接入时签发的新证书直接 active，旧的 active / pending 置 superseded。
--     会话内续期签发的新证书先登记为 pending，旧的 active 照常有效；节点第一次用新证书建流时
--     pending → active、旧 active → superseded（同一事务）。这样节点没收到新证书、或落盘前崩溃，
--     仍能用旧证书连上，不会被锁死。再次续期时没用过的 pending 先置 superseded。后台吊销把 active 与 pending 都置 revoked。
--     认证材料守卫（第 5.3 节）在触发器里：证书、指纹、序列号等签发时写下的列不可改；
--     状态只能 active → superseded / revoked、pending → active / superseded / revoked，终态不可再改；不许删。
--     activated_at：成为 active 的时刻；接入签发的即签发时刻，续期签出的在第一次建流转正时写；pending 为空。
--   - bootstrap_tokens：令牌只存 SHA-256；单次有效（used_at 只由 NULL 写一次，Go 侧 WHERE used_at IS NULL）；
--     记下用掉它的 request_id，节点崩溃后用同一个 request_id、同一把公钥重试时返回同一张证书（enroll_token_id 唯一）。
--     voided_at：吊销身份时作废的未用令牌；作废后不能再用于接入。
--
-- 服务器只软删，所以外键一律 RESTRICT。

-- +goose Up

CREATE TABLE node_ca (
  id          smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  cert_pem    text NOT NULL CHECK (cert_pem LIKE '-----BEGIN CERTIFICATE-----%'),
  -- 封存后的 PKCS#8 私钥（AES-256-GCM，AAD 是 Go 里的固定用途串）
  key_sealed  bytea NOT NULL CHECK (octet_length(key_sealed) > 0),
  -- CA 证书 SubjectPublicKeyInfo DER 的 SHA-256（小写 hex）；安装命令里的指纹是 sha256: 加它
  spki_sha256 text NOT NULL CHECK (spki_sha256 ~ '^[0-9a-f]{64}$'),
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER node_ca_deny_mutation
  BEFORE UPDATE OR DELETE OR TRUNCATE ON node_ca
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

CREATE TABLE bootstrap_tokens (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  server_id       uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
  -- 令牌明文的 SHA-256（小写 hex）；明文只在签发响应里出现一次
  token_sha256    text NOT NULL CHECK (token_sha256 ~ '^[0-9a-f]{64}$'),
  expires_at      timestamptz NOT NULL,
  used_at         timestamptz,
  used_request_id text CHECK (used_request_id ~ '^[A-Za-z0-9._:-]{1,64}$'),
  -- 吊销身份时作废的未用令牌；作废后不能再用于接入
  voided_at       timestamptz,
  -- 签发的管理员；账号删了留空
  issued_by       uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT bootstrap_tokens_hash_key UNIQUE (token_sha256),
  -- TTL 1–60 分钟
  CONSTRAINT bootstrap_tokens_ttl CHECK (
    expires_at > created_at AND expires_at <= created_at + interval '60 minutes'),
  CONSTRAINT bootstrap_tokens_used_shape CHECK ((used_at IS NULL) = (used_request_id IS NULL)),
  CONSTRAINT bootstrap_tokens_end_shape CHECK (used_at IS NULL OR voided_at IS NULL)
);

-- 后台「待用安装令牌」计数
CREATE INDEX bootstrap_tokens_pending_idx ON bootstrap_tokens (server_id, expires_at)
  WHERE used_at IS NULL AND voided_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION bootstrap_tokens_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'bootstrap_tokens 不允许删除' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.id, NEW.server_id, NEW.token_sha256, NEW.expires_at, NEW.created_at)
     IS DISTINCT FROM
     (OLD.id, OLD.server_id, OLD.token_sha256, OLD.expires_at, OLD.created_at) THEN
    RAISE EXCEPTION 'bootstrap_tokens 的认证材料不可改' USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.issued_by IS DISTINCT FROM OLD.issued_by
     AND NOT (OLD.issued_by IS NOT NULL AND NEW.issued_by IS NULL) THEN
    RAISE EXCEPTION 'bootstrap_tokens.issued_by 只允许不变或从非空变成 NULL' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.used_at, NEW.used_request_id) IS DISTINCT FROM (OLD.used_at, OLD.used_request_id) THEN
    IF OLD.used_at IS NOT NULL THEN
      RAISE EXCEPTION 'bootstrap_tokens.used_at 只能从未用写一次' USING ERRCODE = 'restrict_violation';
    END IF;
  END IF;
  IF NEW.voided_at IS DISTINCT FROM OLD.voided_at THEN
    IF OLD.voided_at IS NOT NULL THEN
      RAISE EXCEPTION 'bootstrap_tokens.voided_at 只能写一次' USING ERRCODE = 'restrict_violation';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER bootstrap_tokens_guard
  BEFORE UPDATE OR DELETE ON bootstrap_tokens
  FOR EACH ROW EXECUTE FUNCTION bootstrap_tokens_guard();

CREATE TRIGGER bootstrap_tokens_deny_truncate
  BEFORE TRUNCATE ON bootstrap_tokens
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

CREATE TABLE server_identities (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  server_id         uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
  -- 这台服务器第几次签发（后台展示的 #序号），从 1 递增
  seq               integer NOT NULL CHECK (seq > 0),
  -- 证书序列号（随机 128 位，小写 hex，无前导零）
  cert_serial       text NOT NULL CHECK (cert_serial ~ '^[1-9a-f][0-9a-f]{0,39}$'),
  -- 客户端证书公钥 SubjectPublicKeyInfo DER 的 SHA-256（小写 hex）；接入崩溃重试时按它认同一个节点
  spki_sha256       text NOT NULL CHECK (spki_sha256 ~ '^[0-9a-f]{64}$'),
  cert_pem          text NOT NULL CHECK (cert_pem LIKE '-----BEGIN CERTIFICATE-----%'),
  -- enroll：经接入令牌签发；renew：会话内续期
  source            text NOT NULL CHECK (source IN ('enroll', 'renew')),
  enroll_token_id   uuid REFERENCES bootstrap_tokens(id) ON DELETE RESTRICT,
  enroll_request_id text CHECK (enroll_request_id ~ '^[A-Za-z0-9._:-]{1,64}$'),
  -- pending：续期签出、节点还没用它建过流
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('pending', 'active', 'superseded', 'revoked')),
  -- 成为 active 的时刻；接入签发的即签发时刻，续期签出的在第一次建流转正时写；pending 为空
  activated_at      timestamptz,
  issued_at         timestamptz NOT NULL,
  expires_at        timestamptz NOT NULL,
  superseded_at     timestamptz,
  revoked_at        timestamptz,
  revoked_reason    text CHECK (char_length(revoked_reason) BETWEEN 1 AND 200),
  created_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT server_identities_serial_key UNIQUE (cert_serial),
  CONSTRAINT server_identities_seq_key UNIQUE (server_id, seq),
  -- 一张令牌最多签一份身份（崩溃重试按它找回同一张证书）
  CONSTRAINT server_identities_enroll_token_key UNIQUE (enroll_token_id),
  CONSTRAINT server_identities_validity CHECK (expires_at > issued_at),
  CONSTRAINT server_identities_source_shape CHECK (
    (source = 'enroll') = (enroll_token_id IS NOT NULL)
    AND (enroll_token_id IS NULL) = (enroll_request_id IS NULL)),
  -- 只有续期签出的证书会经过 pending
  CONSTRAINT server_identities_pending_source CHECK (status <> 'pending' OR source = 'renew'),
  CONSTRAINT server_identities_activated_shape CHECK (
    (status <> 'pending' OR activated_at IS NULL)
    AND (status <> 'active' OR activated_at IS NOT NULL)),
  CONSTRAINT server_identities_status_shape CHECK (
    (status IN ('pending', 'active') AND superseded_at IS NULL AND revoked_at IS NULL AND revoked_reason IS NULL)
    OR (status = 'superseded' AND superseded_at IS NOT NULL AND revoked_at IS NULL AND revoked_reason IS NULL)
    OR (status = 'revoked' AND revoked_at IS NOT NULL AND revoked_reason IS NOT NULL AND superseded_at IS NULL))
);

-- 每台服务器至多一份有效身份、一份待生效身份
CREATE UNIQUE INDEX server_identities_active_key ON server_identities (server_id) WHERE status = 'active';
CREATE UNIQUE INDEX server_identities_pending_key ON server_identities (server_id) WHERE status = 'pending';

-- 认证材料守卫：签发时写下的列不可改；状态只能 active → superseded / revoked、
-- pending → active / superseded / revoked（不可逆，终态整行冻结）；不许删
-- +goose StatementBegin
CREATE FUNCTION server_identities_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'server_identities 不允许删除' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.id, NEW.server_id, NEW.seq, NEW.cert_serial, NEW.spki_sha256, NEW.cert_pem,
      NEW.source, NEW.enroll_token_id, NEW.enroll_request_id, NEW.issued_at, NEW.expires_at, NEW.created_at)
     IS DISTINCT FROM
     (OLD.id, OLD.server_id, OLD.seq, OLD.cert_serial, OLD.spki_sha256, OLD.cert_pem,
      OLD.source, OLD.enroll_token_id, OLD.enroll_request_id, OLD.issued_at, OLD.expires_at, OLD.created_at) THEN
    RAISE EXCEPTION 'server_identities 的认证材料不可改' USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.activated_at IS DISTINCT FROM OLD.activated_at
     AND NOT (OLD.activated_at IS NULL AND OLD.status = 'pending' AND NEW.status = 'active') THEN
    RAISE EXCEPTION 'server_identities.activated_at 只在 pending → active 时写一次'
      USING ERRCODE = 'restrict_violation';
  END IF;
  IF NOT ((OLD.status = 'active' AND NEW.status IN ('superseded', 'revoked'))
          OR (OLD.status = 'pending' AND NEW.status IN ('active', 'superseded', 'revoked'))) THEN
    RAISE EXCEPTION 'server_identities 状态只能 active → superseded / revoked、pending → active / superseded / revoked（%→%）',
      OLD.status, NEW.status
      USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER server_identities_guard
  BEFORE UPDATE OR DELETE ON server_identities
  FOR EACH ROW EXECUTE FUNCTION server_identities_guard();

CREATE TRIGGER server_identities_deny_truncate
  BEFORE TRUNCATE ON server_identities
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- +goose Down

DROP TABLE server_identities;
DROP FUNCTION server_identities_guard();
DROP TABLE bootstrap_tokens;
DROP FUNCTION bootstrap_tokens_guard();
DROP TABLE node_ca;
