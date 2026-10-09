-- 节点证书：只追加的签发流水，以及 ACME 账号唯一键带上 EAB KID（对抗审查 10-08；w9cert 第二轮）。
--
-- certificate_issuances  CA 每签出一张证书记一行（租户、证书 id、CA、标识集、是否续期、序列号、时间）。
--                        本地限额对账（domain/certs 的 loadRecentIssuances）改按它数：证书版本会随删证书
--                        一起删，删了重建就数不到，可能撞上 Let's Encrypt「同一组域名每周 5 张」；租约丢失、
--                        结果被丢弃的那张也已经占了 CA 的额度，同样要记。所以：
--                          - 不挂证书外键，删证书不删流水；
--                          - 只追加（触发器 + 收回 UPDATE / DELETE，configure-app-role.sql 末尾再收一次）；
--                          - 由签发协程在 CA 返回证书的那一刻写，与之后落库成不成功无关。
--                        存量：把现有证书版本照抄一行进来，对账从迁移那一刻起不断档。
--                        行数随签发次数增长（每张证书每 1–2 个月一行），保留期清理在 P5 与版本一起做。
-- acme_accounts          原唯一键 (租户, 目录, 联系邮箱) 不含 status 与 eab_kid：账号被 CA 判失效后
--                        没法再注册一个新的，换了 ZeroSSL 的 EAB 也只能撞上旧账号。改成只在 status='valid'
--                        的行上唯一，键里加上 eab_kid（空值按空串）。
--
-- 编号：总协调给的号段是 00149–00150，但主线已经合入 00151；goose 不接受低于已执行最大号的新迁移
-- （已到 00151 的库会报 missing migration），所以取 00152，号段冲突由总协调在合并时裁定。
--
-- 锁与耗时：建一张表；acme_accounts 换唯一键要拿表锁，表里只有几行，毫秒级。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE certificate_issuances (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 签的是哪张证书；不挂外键，证书删了这行留着
  certificate_id  uuid NOT NULL,
  ca              text NOT NULL CHECK (ca IN ('letsencrypt', 'letsencrypt_staging', 'zerossl', 'custom')),
  identifiers     text[] NOT NULL CHECK (cardinality(identifiers) BETWEEN 1 AND 20),
  -- 与上一版标识集合相同（续期）：不计入「每注册域每周新证书」
  is_renewal      boolean NOT NULL,
  -- 这一单带了 ARI replaces（Let's Encrypt 对它免一切限额）
  ari_replaces    boolean NOT NULL DEFAULT false,
  serial          text NOT NULL CHECK (serial ~ '^[0-9a-f]{1,64}$'),
  created_at      timestamptz NOT NULL DEFAULT now()
);
-- 本地限额对账：过去 7 天签了多少张
CREATE INDEX certificate_issuances_recent ON certificate_issuances (tenant_id, created_at);
SELECT app.enable_tenant_rls('certificate_issuances');
SELECT app.make_append_only('certificate_issuances');
GRANT SELECT, INSERT ON certificate_issuances TO aegis_app;
REVOKE UPDATE, DELETE, TRUNCATE ON certificate_issuances FROM aegis_app;
COMMENT ON TABLE certificate_issuances IS
  '每次 CA 签出证书一行，只追加；删证书不删它。本地限额对账按它数。';

INSERT INTO certificate_issuances (tenant_id, certificate_id, ca, identifiers, is_renewal, serial, created_at)
SELECT tenant_id, certificate_id, ca, identifiers, is_renewal, serial, created_at
  FROM certificate_versions;

ALTER TABLE acme_accounts DROP CONSTRAINT acme_accounts_directory_contact_key;
CREATE UNIQUE INDEX acme_accounts_active_key
  ON acme_accounts (tenant_id, directory_url, contact_email, (coalesce(eab_kid, '')))
  WHERE status = 'valid';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 旧唯一键不含 status / eab_kid：同一 (租户, 目录, 邮箱) 已有多行（失效后重注册、换过 EAB）时恢复不了，
-- 拒绝回滚；运维确认旧账号不要了再手工处理。签发流水只是限额对账的计数来源，回滚时整表删掉，
-- 回到旧版本按证书版本数（少数到的只会更早撞 CA 限额，不会多签）。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM acme_accounts GROUP BY tenant_id, directory_url, contact_email HAVING count(*) > 1) THEN
    RAISE EXCEPTION '00152 down refused: acme_accounts has several accounts for the same directory and contact email';
  END IF;
END $$;
-- +goose StatementEnd
DROP INDEX IF EXISTS acme_accounts_active_key;
ALTER TABLE acme_accounts
  ADD CONSTRAINT acme_accounts_directory_contact_key UNIQUE (tenant_id, directory_url, contact_email);
DROP TABLE IF EXISTS certificate_issuances;
