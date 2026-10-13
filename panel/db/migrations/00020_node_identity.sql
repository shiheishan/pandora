-- 节点通道的身份：面板自带的 CA、每台服务器的客户端证书身份、一次性接入令牌。属主 node
-- （docs/architecture.md 第 11.1 节；节点通道契约见 p5 的说明）。
--
--   - node_ca：单行（主键钉死为 1）。私钥经 crypto.Keys.Sealer() 封存后存库，明文私钥不落库。
--     CA 不改不删：换 CA 等于全部服务器重新接入，要另写流程，不靠 UPDATE。
--   - server_identities：一台服务器一份有效身份（部分唯一索引）。接入或续期时签发新证书，
--     旧的置 superseded；后台吊销置 revoked。认证材料守卫（第 5.3 节）在触发器里：
--     证书、指纹、序列号等签发时写下的列不可改；状态只能 active → superseded / revoked，终态不可再改；不许删。
--   - bootstrap_tokens：令牌只存 SHA-256；单次有效（used_at 只由 NULL 写一次，Go 侧 WHERE used_at IS NULL）；
--     记下用掉它的 request_id，节点崩溃后用同一个 request_id 重试时返回同一张证书（enroll_token_id 唯一）。
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
  -- 签发的管理员；账号删了留空
  issued_by       uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT bootstrap_tokens_hash_key UNIQUE (token_sha256),
  -- TTL 1–60 分钟
  CONSTRAINT bootstrap_tokens_ttl CHECK (
    expires_at > created_at AND expires_at <= created_at + interval '60 minutes'),
  CONSTRAINT bootstrap_tokens_used_shape CHECK ((used_at IS NULL) = (used_request_id IS NULL))
);

-- 后台「待用安装令牌」计数
CREATE INDEX bootstrap_tokens_pending_idx ON bootstrap_tokens (server_id, expires_at) WHERE used_at IS NULL;

CREATE TABLE server_identities (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  server_id         uuid NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
  -- 这台服务器第几次签发（后台展示的 #序号），从 1 递增
  seq               integer NOT NULL CHECK (seq > 0),
  -- 证书序列号（随机 128 位，小写 hex，无前导零）
  cert_serial       text NOT NULL CHECK (cert_serial ~ '^[1-9a-f][0-9a-f]{0,39}$'),
  -- 客户端证书公钥 SubjectPublicKeyInfo DER 的 SHA-256（小写 hex）
  spki_sha256       text NOT NULL CHECK (spki_sha256 ~ '^[0-9a-f]{64}$'),
  cert_pem          text NOT NULL CHECK (cert_pem LIKE '-----BEGIN CERTIFICATE-----%'),
  -- 节点送来的 CSR（DER）的 SHA-256：崩溃重试时 CSR 必须相同才返回同一张证书
  csr_sha256        text NOT NULL CHECK (csr_sha256 ~ '^[0-9a-f]{64}$'),
  -- enroll：经接入令牌签发；renew：会话内续期
  source            text NOT NULL CHECK (source IN ('enroll', 'renew')),
  enroll_token_id   uuid REFERENCES bootstrap_tokens(id) ON DELETE RESTRICT,
  enroll_request_id text CHECK (enroll_request_id ~ '^[A-Za-z0-9._:-]{1,64}$'),
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'superseded', 'revoked')),
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
  CONSTRAINT server_identities_status_shape CHECK (
    (status = 'active' AND superseded_at IS NULL AND revoked_at IS NULL AND revoked_reason IS NULL)
    OR (status = 'superseded' AND superseded_at IS NOT NULL AND revoked_at IS NULL AND revoked_reason IS NULL)
    OR (status = 'revoked' AND revoked_at IS NOT NULL AND revoked_reason IS NOT NULL AND superseded_at IS NULL))
);

-- 每台服务器至多一份有效身份
CREATE UNIQUE INDEX server_identities_active_key ON server_identities (server_id) WHERE status = 'active';

-- 认证材料守卫：签发时写下的列不可改；状态只能 active → superseded / revoked（不可逆，终态整行冻结）；不许删
-- +goose StatementBegin
CREATE FUNCTION server_identities_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'server_identities 不允许删除' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.id, NEW.server_id, NEW.seq, NEW.cert_serial, NEW.spki_sha256, NEW.cert_pem, NEW.csr_sha256,
      NEW.source, NEW.enroll_token_id, NEW.enroll_request_id, NEW.issued_at, NEW.expires_at, NEW.created_at)
     IS DISTINCT FROM
     (OLD.id, OLD.server_id, OLD.seq, OLD.cert_serial, OLD.spki_sha256, OLD.cert_pem, OLD.csr_sha256,
      OLD.source, OLD.enroll_token_id, OLD.enroll_request_id, OLD.issued_at, OLD.expires_at, OLD.created_at) THEN
    RAISE EXCEPTION 'server_identities 的认证材料不可改' USING ERRCODE = 'restrict_violation';
  END IF;
  IF OLD.status <> 'active' OR NEW.status NOT IN ('superseded', 'revoked') THEN
    RAISE EXCEPTION 'server_identities 状态只能 active → superseded / revoked（%→%）', OLD.status, NEW.status
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
DROP TABLE node_ca;
