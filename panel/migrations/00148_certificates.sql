-- 节点证书集中签发 P2 之二：证书、证书版本与签发订单（用户 2026-10-07 定做面板集中签发，
-- 10-08 定首发只做 Cloudflare DNS-01、不做 IP 证书与 HTTP-01 / TLS-ALPN-01；w9cert）。
--
-- certificates          后台建的证书资源：标识（域名，可含通配符）、DNS 凭据、密钥类型；以及签发
--                       worker 维护的状态（当前版本、到期、续期时间、ARI 窗口、退避与失败计数）。
--                       证书 id 在续期时不变，节点配置以后只引用这个 id（P3）。
-- certificate_versions  每签出一张证书插一行，只插不改：没有 UPDATE 授权，另有触发器拒绝 UPDATE
--                       （configure-app-role.sql 的整表重授之后再收一次 UPDATE）。私钥是 PKCS#8 DER
--                       的信封密文，AAD = certificate:<tenant>:<cert>:<version>:private_key。删证书
--                       与以后的保留期清理整行删除，所以保留 DELETE 授权。
-- certificate_orders    签发任务兼分布式锁：部分唯一索引保证同一张证书同时最多一张 queued / running
--                       的订单；worker 用 FOR UPDATE SKIP LOCKED 认领并写租约（lease_owner /
--                       lease_until），签发跑在事务外、每 30 秒续租，进程挂了租约一过别的 worker
--                       接手。订单表本身就是签发历史，不另建追加写表。
--
-- 读写它们的都是 domain/certs（服务 + worker）。锁与耗时：只建三张空表，毫秒级。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE certificates (
  id                    uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name                  text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 64),
  -- 小写、去重、排好序的域名；通配符只允许最左一级（应用层校验格式）
  identifiers           text[] NOT NULL CHECK (cardinality(identifiers) BETWEEN 1 AND 20
                                               AND array_position(identifiers, NULL) IS NULL),
  -- 首发只有 DNS-01（用户 10-08 定）；以后加验证方式放宽这条 CHECK
  challenge             text NOT NULL DEFAULT 'dns-01' CHECK (challenge IN ('dns-01')),
  dns_credential_id     uuid NOT NULL,
  key_type              text NOT NULL DEFAULT 'ecdsa-p256' CHECK (key_type IN ('ecdsa-p256', 'rsa-2048')),
  -- pending 还没签出过；active 自动续期中；paused 停止自动重试（手动或连续失败 3 次）；
  -- blocked_credential 凭据预检失败，凭据重新校验通过后自动恢复
  status                text NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'active', 'paused', 'blocked_credential')),
  paused_reason         text CHECK (paused_reason IN ('manual', 'failures')),
  current_version_id    uuid,
  not_before            timestamptz,
  not_after             timestamptz,
  -- 到这个时间就排续期订单；有 ARI 窗口时取窗口内的随机点，否则剩余寿命 1/3 时
  renew_after           timestamptz,
  ari_window_start      timestamptz,
  ari_window_end        timestamptz,
  ari_checked_at        timestamptz,
  -- 退避：失败或 CA 限流后，到这个时间之前不再排新订单
  next_attempt_at       timestamptz,
  consecutive_failures  int NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  last_error_code       text CHECK (char_length(last_error_code) <= 64),
  last_error            text CHECK (char_length(last_error) <= 2048),
  -- 已经在后台提示过的到期等级（0 无、1 黄、2 红），只在上升时记一次审计
  alerted_level         smallint NOT NULL DEFAULT 0 CHECK (alerted_level BETWEEN 0 AND 2),
  row_version           bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by            uuid,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT certificates_tenant_id_key UNIQUE (tenant_id, id),
  CONSTRAINT certificates_dns_credential_fk FOREIGN KEY (tenant_id, dns_credential_id)
    REFERENCES dns_credentials (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT certificates_paused_reason CHECK ((status = 'paused') = (paused_reason IS NOT NULL)),
  CONSTRAINT certificates_current_version CHECK (
    (current_version_id IS NULL) = (not_after IS NULL) AND (current_version_id IS NULL) = (not_before IS NULL)),
  CONSTRAINT certificates_pending_has_no_version CHECK (status <> 'pending' OR current_version_id IS NULL)
);
CREATE UNIQUE INDEX certificates_name_key ON certificates (tenant_id, lower(name));
-- 排续期：只看自动续期中的证书
CREATE INDEX certificates_due ON certificates (tenant_id, renew_after)
  WHERE status IN ('pending', 'active');
-- 凭据被引用时不能删、凭据恢复时找回被它卡住的证书
CREATE INDEX certificates_dns_credential ON certificates (tenant_id, dns_credential_id);
CREATE TRIGGER trg_certificates_updated_at BEFORE UPDATE ON certificates
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
SELECT app.enable_tenant_rls('certificates');
GRANT SELECT, INSERT, UPDATE, DELETE ON certificates TO aegis_app;
COMMENT ON TABLE certificates IS
  '面板集中签发的节点证书资源；id 在续期时不变，签发状态由 aegis-admin 的证书 worker 维护。';

CREATE TABLE certificate_versions (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  certificate_id      uuid NOT NULL,
  version             int NOT NULL CHECK (version > 0),
  acme_account_id     uuid NOT NULL,
  ca                  text NOT NULL CHECK (ca IN ('letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom')),
  -- 签发时的标识集合（小写、排序）；本地限额对账按它数
  identifiers         text[] NOT NULL CHECK (cardinality(identifiers) BETWEEN 1 AND 20),
  -- 与上一版标识集合完全相同（续期）：不计入「每注册域每周新证书」限额
  is_renewal          boolean NOT NULL,
  serial              text NOT NULL CHECK (serial ~ '^[0-9a-f]{1,64}$'),
  chain_pem           text NOT NULL CHECK (char_length(chain_pem) BETWEEN 1 AND 65536),
  private_key_sealed  bytea NOT NULL,
  public_key_sha256   bytea NOT NULL CHECK (octet_length(public_key_sha256) = 32),
  chain_sha256        bytea NOT NULL CHECK (octet_length(chain_sha256) = 32),
  not_before          timestamptz NOT NULL,
  not_after           timestamptz NOT NULL,
  -- RFC 9773 的 certID，续期时作为 replaces 发给 CA
  ari_cert_id         text CHECK (char_length(ari_cert_id) <= 256),
  cert_url            text CHECK (char_length(cert_url) <= 512),
  order_id            uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT certificate_versions_tenant_id_key UNIQUE (tenant_id, id),
  CONSTRAINT certificate_versions_version_key UNIQUE (tenant_id, certificate_id, version),
  CONSTRAINT certificate_versions_validity CHECK (not_after > not_before),
  CONSTRAINT certificate_versions_certificate_fk FOREIGN KEY (tenant_id, certificate_id)
    REFERENCES certificates (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT certificate_versions_account_fk FOREIGN KEY (tenant_id, acme_account_id)
    REFERENCES acme_accounts (tenant_id, id) ON DELETE RESTRICT
);
-- 本地限额对账：过去 7 天签了多少张
CREATE INDEX certificate_versions_recent ON certificate_versions (tenant_id, created_at);
-- 只插不改：语句级触发器挡住任何 UPDATE（DELETE 留给删证书与保留期清理）
CREATE TRIGGER trg_certificate_versions_immutable BEFORE UPDATE ON certificate_versions
  FOR EACH STATEMENT EXECUTE FUNCTION app.deny_mutation();
SELECT app.enable_tenant_rls('certificate_versions');
GRANT SELECT, INSERT, DELETE ON certificate_versions TO aegis_app;
REVOKE UPDATE, TRUNCATE ON certificate_versions FROM aegis_app;
COMMENT ON TABLE certificate_versions IS
  '每次签出的证书一行，只插不改；私钥是信封密文。删证书与保留期清理整行删除。';

-- 证书的当前版本必须是它自己的版本（同租户复合外键；删证书时先置空再删版本）
ALTER TABLE certificates ADD CONSTRAINT certificates_current_version_fk
  FOREIGN KEY (tenant_id, current_version_id) REFERENCES certificate_versions (tenant_id, id)
  ON DELETE RESTRICT;

CREATE TABLE certificate_orders (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  certificate_id   uuid NOT NULL,
  reason           text NOT NULL CHECK (reason IN ('initial', 'renewal', 'ari', 'manual')),
  state            text NOT NULL DEFAULT 'queued'
                     CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  -- 实际下单的 CA（备用 CA 切换后是 zerossl）
  ca               text CHECK (ca IN ('letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom')),
  replaces_ari_id  text CHECK (char_length(replaces_ari_id) <= 256),
  -- 被认领的次数；租约归属按 (lease_owner, attempt) 判断
  attempt          int NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  lease_owner      text CHECK (char_length(lease_owner) <= 128),
  lease_until      timestamptz,
  error_code       text CHECK (char_length(error_code) <= 64),
  error_detail     text CHECK (char_length(error_detail) <= 2048),
  version_id       uuid,
  created_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  started_at       timestamptz,
  finished_at      timestamptz,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT certificate_orders_tenant_id_key UNIQUE (tenant_id, id),
  CONSTRAINT certificate_orders_certificate_fk FOREIGN KEY (tenant_id, certificate_id)
    REFERENCES certificates (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT certificate_orders_finished CHECK (
    (state IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
  CONSTRAINT certificate_orders_running_lease CHECK (
    state <> 'running' OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL)),
  CONSTRAINT certificate_orders_succeeded_version CHECK ((state = 'succeeded') = (version_id IS NOT NULL))
);
-- 分布式锁：同一张证书同时最多一张进行中的订单
CREATE UNIQUE INDEX certificate_orders_one_active
  ON certificate_orders (tenant_id, certificate_id) WHERE state IN ('queued', 'running');
-- worker 认领：只有没做完的订单进这条索引，它永远很小
CREATE INDEX certificate_orders_open ON certificate_orders (tenant_id, created_at)
  WHERE state IN ('queued', 'running');
-- 详情页的订单历史
CREATE INDEX certificate_orders_by_certificate
  ON certificate_orders (tenant_id, certificate_id, created_at DESC);
CREATE TRIGGER trg_certificate_orders_updated_at BEFORE UPDATE ON certificate_orders
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
SELECT app.enable_tenant_rls('certificate_orders');
GRANT SELECT, INSERT, UPDATE, DELETE ON certificate_orders TO aegis_app;
COMMENT ON TABLE certificate_orders IS
  '证书签发任务兼分布式锁：部分唯一索引保证一张证书同时最多一张进行中的订单，worker 以租约认领。';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 签出过的证书版本带着私钥密文与 CA 的签发记录，回滚不能悄悄丢掉：有版本就拒绝，
-- 运维先在后台删掉证书（或确认不要了手工清表）再回滚。没有版本时照常删表。
-- +goose StatementBegin
DO $$
BEGIN
  IF to_regclass('public.certificate_versions') IS NOT NULL
     AND EXISTS (SELECT 1 FROM public.certificate_versions) THEN
    RAISE EXCEPTION '00148 down refused: certificate_versions still holds issued certificates; delete the certificates in the admin first';
  END IF;
END $$;
-- +goose StatementEnd
DROP TABLE IF EXISTS certificate_orders;
ALTER TABLE IF EXISTS certificates DROP CONSTRAINT IF EXISTS certificates_current_version_fk;
DROP TABLE IF EXISTS certificate_versions;
DROP TABLE IF EXISTS certificates;
