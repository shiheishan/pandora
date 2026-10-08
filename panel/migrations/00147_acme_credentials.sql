-- 节点证书集中签发 P2 之一：DNS 凭据、ACME 账号与权限码（用户 2026-10-07 定做面板集中签发，
-- 10-08 定 DNS-01 首发 Cloudflare、阿里云 DNS、腾讯云 DNSPod 三家，ZeroSSL 备用 CA 可选默认关；w9cert）。
--
-- dns_credentials  后台录入的 DNS 提供方凭据（Cloudflare API 令牌、阿里云 AccessKey、腾讯云 SecretId/Key）。
--                  凭据只存信封密文（AAD 绑定租户与凭据 id），读接口只回末四位；校验结果
--                  （看得到几个 zone、最近一次失败原因）存明文列。
--                  写：domain/certs 的凭据服务；读：后台列表与签发 worker（预检 + lego 提供方）。
-- acme_accounts    面板向 CA 注册的 ACME 账号。账号私钥只存信封密文；同一目录 + 联系邮箱只有
--                  一个账号，worker 第一次签发时注册并落库，之后复用。
--
-- 权限码：node.certificate.read 授给已有 node.read 的角色，node.certificate.write 授给已有
-- node.provision 的角色（与新建节点同一批人）。ACME 设置（联系邮箱、测试 CA、ZeroSSL EAB）
-- 放 system_settings 的 acme.* 键，不另建表。
--
-- 锁与耗时：只建两张空表、插几行权限，毫秒级。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE dns_credentials (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name           text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 64),
  -- 以后加提供方放宽这条 CHECK
  provider       text NOT NULL CHECK (provider IN ('cloudflare', 'alidns', 'tencentcloud')),
  -- 预检与最小权限检查用的 zone 名（小写、不带末尾点）
  zone           text NOT NULL CHECK (zone ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'
                                      AND char_length(zone) <= 253),
  -- 信封密文：按提供方的 JSON（cloudflare {api_token}；alidns {access_key_id, access_key_secret}；
  -- tencentcloud {secret_id, secret_key}），AAD = dns_credential:<tenant>:<id>
  secret_sealed  bytea NOT NULL,
  -- 令牌或密钥 ID 的末四位，界面显示 ****abcd；不是秘密
  secret_hint    text NOT NULL CHECK (char_length(secret_hint) <= 8),
  verify_status  text NOT NULL DEFAULT 'unverified'
                   CHECK (verify_status IN ('unverified', 'ok', 'error')),
  verified_at    timestamptz,
  verify_error   text CHECK (char_length(verify_error) <= 512),
  -- 令牌看得到的 zone 数；多于 1 个时界面提示「令牌范围不是最小权限」
  visible_zones  int CHECK (visible_zones >= 0),
  row_version    bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dns_credentials_tenant_id_key UNIQUE (tenant_id, id),
  CONSTRAINT dns_credentials_verified CHECK (verify_status <> 'ok' OR verified_at IS NOT NULL)
);
-- 同一租户内名字不重复（不分大小写）
CREATE UNIQUE INDEX dns_credentials_name_key ON dns_credentials (tenant_id, lower(name));
CREATE TRIGGER trg_dns_credentials_updated_at BEFORE UPDATE ON dns_credentials
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
SELECT app.enable_tenant_rls('dns_credentials');
GRANT SELECT, INSERT, UPDATE, DELETE ON dns_credentials TO aegis_app;
COMMENT ON TABLE dns_credentials IS
  '节点证书 DNS-01 验证用的 DNS 提供方凭据；令牌只存信封密文，读接口只回末四位。';

CREATE TABLE acme_accounts (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- custom 只在非生产环境的目录覆盖（pebble）下出现
  ca                 text NOT NULL
                       CHECK (ca IN ('letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom')),
  directory_url      text NOT NULL CHECK (directory_url ~ '^https://' AND char_length(directory_url) <= 512),
  -- 空串表示注册时不带联系方式（Let's Encrypt 允许）
  contact_email      text NOT NULL DEFAULT '' CHECK (char_length(contact_email) <= 254),
  -- 信封密文：PKCS#8 DER 账号私钥，AAD = acme_account:<tenant>:<id>
  account_key_sealed bytea NOT NULL,
  account_url        text NOT NULL CHECK (account_url ~ '^https://' AND char_length(account_url) <= 512),
  -- ZeroSSL 注册时用的 EAB key id（不是秘密；HMAC 只在设置里存密文）
  eab_kid            text CHECK (char_length(eab_kid) <= 256),
  status             text NOT NULL DEFAULT 'valid' CHECK (status IN ('valid', 'deactivated')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT acme_accounts_tenant_id_key UNIQUE (tenant_id, id),
  CONSTRAINT acme_accounts_directory_contact_key UNIQUE (tenant_id, directory_url, contact_email)
);
CREATE TRIGGER trg_acme_accounts_updated_at BEFORE UPDATE ON acme_accounts
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
SELECT app.enable_tenant_rls('acme_accounts');
GRANT SELECT, INSERT, UPDATE ON acme_accounts TO aegis_app;
COMMENT ON TABLE acme_accounts IS
  '面板向 CA 注册的 ACME 账号；账号私钥只存信封密文，签发过的证书版本按外键引用它。';

-- +goose StatementBegin
INSERT INTO permissions (code, domain, description, high_risk)
VALUES ('node.certificate.read',  'node', '查看节点证书、DNS 凭据与 ACME 设置', false),
       ('node.certificate.write', 'node', '管理节点证书、DNS 凭据与 ACME 设置', true)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT rp.role_id, 'node.certificate.read'
  FROM role_permissions rp
 WHERE rp.permission_code = 'node.read'
ON CONFLICT (role_id, permission_code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT rp.role_id, 'node.certificate.write'
  FROM role_permissions rp
 WHERE rp.permission_code = 'node.provision'
ON CONFLICT (role_id, permission_code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 凭据与账号都只是签发的输入：删掉不丢证书以外的业务数据（证书表在 00148，回滚它时先删）。
-- 已写进 system_settings 的 acme.* 键保留：旧版本不读它们。
-- +goose StatementBegin
DELETE FROM role_permissions
 WHERE permission_code IN ('node.certificate.read', 'node.certificate.write');
DELETE FROM permissions
 WHERE code IN ('node.certificate.read', 'node.certificate.write');
-- +goose StatementEnd
DROP TABLE IF EXISTS acme_accounts;
DROP TABLE IF EXISTS dns_credentials;
