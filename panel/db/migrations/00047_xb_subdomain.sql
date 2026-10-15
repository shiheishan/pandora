-- +goose Up
-- 做什么：订阅域名分组，属 subscription 模块；用户 2026-10-14 定，取代 settings 的 subscription.link_base_urls。
-- 锁与耗时：只建三张新表与索引，瞬间完成；对 user_groups 加外键要拿它的 SHARE ROW EXCLUSIVE 锁，lock_timeout 5s 拿不到就失败。
-- 预检：无需，新表。
-- 失败回滚：整个迁移一个事务，失败全部回滚，重跑即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- 订阅域名组：名称、是否缺省与排序（最多 20 组，最多一个缺省组）。
CREATE TABLE subscription_domain_groups (
  id         uuid PRIMARY KEY,
  name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40 AND name = btrim(name)),
  is_default boolean NOT NULL DEFAULT false,
  position   smallint NOT NULL CHECK (position BETWEEN 0 AND 19),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT subscription_domain_groups_name_key UNIQUE (name),
  CONSTRAINT subscription_domain_groups_position_key UNIQUE (position)
);
CREATE UNIQUE INDEX subscription_domain_groups_one_default ON subscription_domain_groups ((true)) WHERE is_default;

-- 组内域名：规范化后的 https 地址、基础权重（1–100）与组内排序（最多 10 个）。
CREATE TABLE subscription_domains (
  group_id uuid NOT NULL REFERENCES subscription_domain_groups(id) ON DELETE CASCADE,
  base_url text NOT NULL CHECK (char_length(base_url) <= 263
             AND base_url ~ '^https://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$'),
  weight   smallint NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 100),
  position smallint NOT NULL CHECK (position BETWEEN 0 AND 9),
  CONSTRAINT subscription_domains_pkey PRIMARY KEY (group_id, base_url),
  CONSTRAINT subscription_domains_group_position_key UNIQUE (group_id, position)
);

-- 用户组到订阅域名组的映射（一个用户组最多进一个域名组）。
CREATE TABLE subscription_domain_group_user_groups (
  user_group_id uuid PRIMARY KEY REFERENCES user_groups(id) ON DELETE CASCADE,
  group_id      uuid NOT NULL REFERENCES subscription_domain_groups(id) ON DELETE CASCADE
);
CREATE INDEX subscription_domain_group_user_groups_group_idx ON subscription_domain_group_user_groups (group_id);

-- +goose Down
-- 回滚丢掉后台配的订阅域名（回滚后用不到）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

DROP TABLE subscription_domain_group_user_groups;
DROP TABLE subscription_domains;
DROP TABLE subscription_domain_groups;
