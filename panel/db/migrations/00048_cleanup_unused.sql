-- +goose Up
-- 下线「面板集中签发节点证书」（certs 模块，用户 10-14 同意；签出的证书一直没有路径送到节点）：
-- 删 00025 建的六张表（dns_credentials、acme_accounts、certificates、certificate_versions、certificate_orders、
-- certificate_issuances，连同它们的索引与 deny_mutation 触发器），以及 settings 里 ACME 设置的 acme.* 键。
-- 以后要做，按节点 gRPC 会话下发重新设计，不恢复这套表。
-- 主题的 custom_css 没有库列（00029 起就没建），这里不涉及。
--
-- 锁与耗时：DROP TABLE 要各表的 ACCESS EXCLUSIVE；这些表只有已下线的 certs 模块读写，本版本的进程不再碰，
--   且没有生产部署，表是空的或只有几行，瞬间完成。
-- 预检（可选，看会丢多少数据）：SELECT count(*) FROM certificates; SELECT count(*) FROM dns_credentials;
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，停掉旧版本 worker 后重试。
-- 数据不可恢复：Down 只重建表结构（与 00025 相同），凭据、ACME 账号、证书与私钥密文、签发流水、acme.* 设置都不会回来。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

DELETE FROM settings WHERE key LIKE 'acme.%';

DROP TABLE certificate_issuances;
DROP TABLE certificate_orders;
-- certificates 与 certificate_versions 互相引用：先拆当前版本的复合外键再删
ALTER TABLE certificates DROP CONSTRAINT certificates_current_version_fk;
DROP TABLE certificate_versions;
DROP TABLE certificates;
DROP TABLE acme_accounts;
DROP TABLE dns_credentials;

-- +goose Down
-- 按 00025_certs.sql 原样重建表结构（数据不恢复，见文件头）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE dns_credentials (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  name           text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 64),
  -- 以后加提供方放宽这条 CHECK
  provider       text NOT NULL CHECK (provider IN ('cloudflare', 'alidns', 'tencentcloud')),
  -- 预检与最小权限检查用的 zone 名（小写、不带末尾点）
  zone           text NOT NULL CHECK (zone ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'
                                      AND char_length(zone) <= 253),
  secret_sealed  bytea NOT NULL CHECK (octet_length(secret_sealed) > 0),
  -- 令牌或密钥 ID 的末四位，界面显示 ****abcd；不是秘密
  secret_hint    text NOT NULL CHECK (char_length(secret_hint) <= 8),
  verify_status  text NOT NULL DEFAULT 'unverified' CHECK (verify_status IN ('unverified', 'ok', 'error')),
  verified_at    timestamptz,
  verify_error   text CHECK (char_length(verify_error) <= 512),
  -- 凭据看得到的 zone 数；多于 1 个时界面提示不是最小权限
  visible_zones  int CHECK (visible_zones >= 0),
  -- 每次改名、改 zone、换密钥加一；校验与签发预检的结论只在凭据没被改过时才落库
  row_version    bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dns_credentials_verified CHECK (verify_status <> 'ok' OR verified_at IS NOT NULL)
);
-- 名字不重复（不分大小写）
CREATE UNIQUE INDEX dns_credentials_name_key ON dns_credentials (lower(name));

CREATE TABLE acme_accounts (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  -- custom 只在非生产环境的目录覆盖（pebble）下出现
  ca                 text NOT NULL CHECK (ca IN ('letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom')),
  directory_url      text NOT NULL CHECK (directory_url ~ '^https://' AND char_length(directory_url) <= 512),
  -- 空串表示注册时不带联系方式（Let's Encrypt 允许）
  contact_email      text NOT NULL DEFAULT '' CHECK (char_length(contact_email) <= 254),
  account_key_sealed bytea NOT NULL CHECK (octet_length(account_key_sealed) > 0),
  account_url        text NOT NULL CHECK (account_url ~ '^https://' AND char_length(account_url) <= 512),
  -- ZeroSSL 注册时用的 EAB key id（不是秘密；HMAC 封存在设置里）
  eab_kid            text CHECK (char_length(eab_kid) <= 256),
  status             text NOT NULL DEFAULT 'valid' CHECK (status IN ('valid', 'deactivated')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
-- 同一目录 + 联系邮箱 + EAB KID 只有一个有效账号；CA 说账号失效时标 deactivated，下一单重新注册
CREATE UNIQUE INDEX acme_accounts_active_key
  ON acme_accounts (directory_url, contact_email, (coalesce(eab_kid, '')))
  WHERE status = 'valid';

CREATE TABLE certificates (
  id                    uuid PRIMARY KEY DEFAULT uuidv7(),
  name                  text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 64),
  identifiers           jsonb NOT NULL CHECK (jsonb_typeof(identifiers) = 'array'
                                              AND jsonb_array_length(identifiers) BETWEEN 1 AND 20),
  -- 只做 DNS-01（用户 10-08 定）；以后加验证方式放宽这条 CHECK
  challenge             text NOT NULL DEFAULT 'dns-01' CHECK (challenge IN ('dns-01')),
  dns_credential_id     uuid NOT NULL REFERENCES dns_credentials(id) ON DELETE RESTRICT,
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
  -- 已经记过审计的到期等级（0 无、1 黄、2 红），只在上升时记一次
  alerted_level         smallint NOT NULL DEFAULT 0 CHECK (alerted_level BETWEEN 0 AND 2),
  row_version           bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by            uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT certificates_paused_reason CHECK ((status = 'paused') = (paused_reason IS NOT NULL)),
  CONSTRAINT certificates_current_version CHECK (
    (current_version_id IS NULL) = (not_after IS NULL) AND (current_version_id IS NULL) = (not_before IS NULL)),
  CONSTRAINT certificates_pending_has_no_version CHECK (status <> 'pending' OR current_version_id IS NULL),
  CONSTRAINT certificates_validity CHECK (not_after IS NULL OR not_after > not_before)
);
CREATE UNIQUE INDEX certificates_name_key ON certificates (lower(name));
-- 排续期：只看自动续期中的证书
CREATE INDEX certificates_due_idx ON certificates (renew_after) WHERE status IN ('pending', 'active');
-- 凭据被引用时不能删、凭据恢复时找回被它卡住的证书
CREATE INDEX certificates_dns_credential_idx ON certificates (dns_credential_id);

CREATE TABLE certificate_versions (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  certificate_id      uuid NOT NULL REFERENCES certificates(id) ON DELETE RESTRICT,
  version             int NOT NULL CHECK (version > 0),
  acme_account_id     uuid NOT NULL REFERENCES acme_accounts(id) ON DELETE RESTRICT,
  ca                  text NOT NULL CHECK (ca IN ('letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom')),
  -- 签发时的标识集合（小写、排序）
  identifiers         jsonb NOT NULL CHECK (jsonb_typeof(identifiers) = 'array'
                                            AND jsonb_array_length(identifiers) BETWEEN 1 AND 20),
  -- 与上一版标识集合完全相同（续期）：不计入「每注册域每周新证书」
  is_renewal          boolean NOT NULL,
  serial              text NOT NULL CHECK (serial ~ '^[0-9a-f]{1,64}$'),
  chain_pem           text NOT NULL CHECK (char_length(chain_pem) BETWEEN 1 AND 65536),
  private_key_sealed  bytea NOT NULL CHECK (octet_length(private_key_sealed) > 0),
  public_key_sha256   bytea NOT NULL CHECK (octet_length(public_key_sha256) = 32),
  chain_sha256        bytea NOT NULL CHECK (octet_length(chain_sha256) = 32),
  not_before          timestamptz NOT NULL,
  not_after           timestamptz NOT NULL,
  -- RFC 9773 的 certID，续期时作为 replaces 发给 CA
  ari_cert_id         text CHECK (char_length(ari_cert_id) <= 256),
  cert_url            text CHECK (char_length(cert_url) <= 512),
  -- 签出它的订单（不挂外键，见文件头）
  order_id            uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT certificate_versions_version_key UNIQUE (certificate_id, version),
  CONSTRAINT certificate_versions_validity CHECK (not_after > not_before)
);
-- 版本只插不改：UPDATE、TRUNCATE 一律拒绝；DELETE 留给删证书（连同全部版本与私钥密文一起删）
CREATE TRIGGER certificate_versions_immutable
  BEFORE UPDATE OR TRUNCATE ON certificate_versions
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 证书的当前版本必须是存在的版本，且属于这张证书（复合外键）；删证书时先置空再删版本
ALTER TABLE certificate_versions ADD CONSTRAINT certificate_versions_cert_id_key UNIQUE (certificate_id, id);
ALTER TABLE certificates ADD CONSTRAINT certificates_current_version_fk
  FOREIGN KEY (id, current_version_id) REFERENCES certificate_versions (certificate_id, id) ON DELETE RESTRICT;

CREATE TABLE certificate_orders (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  certificate_id   uuid NOT NULL REFERENCES certificates(id) ON DELETE RESTRICT,
  reason           text NOT NULL CHECK (reason IN ('initial', 'renewal', 'ari', 'manual')),
  state            text NOT NULL DEFAULT 'queued'
                     CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  -- 实际下单的 CA（切到备用 CA 后是 zerossl）
  ca               text CHECK (ca IN ('letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom')),
  replaces_ari_id  text CHECK (char_length(replaces_ari_id) <= 256),
  -- 被认领的次数（栅栏令牌）；落库时核对订单仍是 running 且 attempt 没变，否则丢弃这次结果
  attempt          int NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  -- 认领有效期（签发任务的超时加余量）；过了还在 running 就当签发任务已中断，巡检把订单记为失败
  lease_until      timestamptz,
  error_code       text CHECK (char_length(error_code) <= 64),
  error_detail     text CHECK (char_length(error_detail) <= 2048),
  version_id       uuid REFERENCES certificate_versions(id) ON DELETE RESTRICT,
  created_by       uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  started_at       timestamptz,
  finished_at      timestamptz,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT certificate_orders_finished CHECK (
    (state IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
  CONSTRAINT certificate_orders_running_lease CHECK (
    state <> 'running' OR (lease_until IS NOT NULL AND started_at IS NOT NULL AND attempt > 0)),
  CONSTRAINT certificate_orders_succeeded_version CHECK ((state = 'succeeded') = (version_id IS NOT NULL))
);
-- 同一张证书同时最多一张进行中的订单
CREATE UNIQUE INDEX certificate_orders_one_active
  ON certificate_orders (certificate_id) WHERE state IN ('queued', 'running');
-- 巡检入队：只有没做完的订单进这条索引，它永远很小
CREATE INDEX certificate_orders_open_idx ON certificate_orders (created_at) WHERE state IN ('queued', 'running');
-- 详情页的订单历史
CREATE INDEX certificate_orders_by_certificate_idx ON certificate_orders (certificate_id, created_at DESC);

CREATE TABLE certificate_issuances (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 签的是哪张证书；不挂外键，证书删了这行留着
  certificate_id  uuid NOT NULL,
  ca              text NOT NULL CHECK (ca IN ('letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom')),
  identifiers     jsonb NOT NULL CHECK (jsonb_typeof(identifiers) = 'array'
                                        AND jsonb_array_length(identifiers) BETWEEN 1 AND 20),
  -- 与上一版标识集合相同（续期）：不计入「每注册域每周新证书」
  is_renewal      boolean NOT NULL,
  -- 这一单带了 ARI replaces（Let's Encrypt 对它免一切限额）
  ari_replaces    boolean NOT NULL DEFAULT false,
  serial          text NOT NULL CHECK (serial ~ '^[0-9a-f]{1,64}$'),
  created_at      timestamptz NOT NULL DEFAULT now()
);
-- 本地限额对账：过去 7 天签了多少张
CREATE INDEX certificate_issuances_created_at_idx ON certificate_issuances (created_at);
-- 签发流水只追加（00001_base 的约定：语句级，连 TRUNCATE 一起挡）
CREATE TRIGGER certificate_issuances_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON certificate_issuances
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();
