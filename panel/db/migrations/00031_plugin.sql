-- +goose Up
-- plugin 模块：插件钩子（plugin_hooks）与出站投递队列（plugin_hook_deliveries）。依赖 00005_identity.sql 的 users。
--
-- 插件以出站 webhook 订阅面板事件：业务事务里按订阅关系写 queued 投递（与业务变更同生共死），
-- 提交后由 plugin:dispatch 认领，用钩子的签名密钥做 HMAC-SHA256 签名后 POST 给插件。
-- 事件名白名单在 plugin service（加事件不必迁移）；签名密钥用 Sealer 封存（AAD 绑定钩子 code），只写不读。
-- 投递记录保留 90 天，由 retention:plugin 清理。

CREATE TABLE plugin_hooks (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  code          text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_-]{1,38}$'),
  name          text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  description   text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  enabled       boolean NOT NULL DEFAULT false,
  -- 订阅的事件名（jsonb 字符串数组）
  events        jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(events) = 'array'),
  endpoint_url  text NOT NULL CHECK (endpoint_url ~ '^https?://' AND char_length(endpoint_url) <= 2048),
  -- 签名密钥的封存密文：新建时没给就由面板生成，所以每个钩子都有
  secret_enc    bytea NOT NULL CHECK (octet_length(secret_enc) > 0),
  timeout_ms    integer NOT NULL DEFAULT 5000 CHECK (timeout_ms BETWEEN 500 AND 30000),
  max_attempts  integer NOT NULL DEFAULT 5 CHECK (max_attempts BETWEEN 1 AND 10),
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT plugin_hooks_code_key UNIQUE (code),
  -- 启用的钩子至少订阅一个事件，否则永远不会触发
  CONSTRAINT plugin_hooks_enabled_events CHECK (NOT enabled OR jsonb_array_length(events) > 0)
);

-- 出站队列。钩子删掉时它的投递记录一并删除（与旧库外键相同）。
CREATE TABLE plugin_hook_deliveries (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  hook_id          uuid NOT NULL REFERENCES plugin_hooks(id) ON DELETE CASCADE,
  event            text NOT NULL CHECK (char_length(event) BETWEEN 1 AND 64),
  -- 同一事件对同一钩子只投一次：业务侧重复触发（支付回调重放、扫描重跑）不让插件收到两遍
  dedupe_key       text NOT NULL CHECK (char_length(dedupe_key) BETWEEN 1 AND 200),
  payload          jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
  status           text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sent', 'failed')),
  -- 认领即算一次尝试（处理中途崩溃的行也会走向上限）
  attempts         integer NOT NULL DEFAULT 0,
  max_attempts     integer NOT NULL CHECK (max_attempts BETWEEN 1 AND 10),
  -- queued 行下次可认领的时间：新行即刻；认领时推到租约末；失败后按退避推后。终态为空
  next_retry_at    timestamptz DEFAULT now(),
  -- 最后一次尝试对方回的 HTTP 状态码；请求没发出去或没收到响应时为空
  response_code    integer CHECK (response_code BETWEEN 100 AND 599),
  error_message    text NOT NULL DEFAULT '' CHECK (char_length(error_message) <= 500),
  -- 最后一次尝试的往返耗时；请求没发出去时为空
  last_duration_ms integer CHECK (last_duration_ms >= 0),
  sent_at          timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT plugin_hook_deliveries_dedupe UNIQUE (hook_id, dedupe_key),
  CONSTRAINT plugin_hook_deliveries_attempts_range CHECK (attempts BETWEEN 0 AND max_attempts),
  CONSTRAINT plugin_hook_deliveries_sent_at CHECK ((status = 'sent') = (sent_at IS NOT NULL)),
  CONSTRAINT plugin_hook_deliveries_retry_state CHECK ((status = 'queued') = (next_retry_at IS NOT NULL))
);

-- 认领：只扫 queued 行
CREATE INDEX plugin_hook_deliveries_due ON plugin_hook_deliveries (next_retry_at) WHERE status = 'queued';
-- 后台投递记录（按钩子倒序）与列表的近 7 天计数
CREATE INDEX plugin_hook_deliveries_hook_created ON plugin_hook_deliveries (hook_id, created_at DESC);
-- 列表的「最后送达」
CREATE INDEX plugin_hook_deliveries_hook_sent ON plugin_hook_deliveries (hook_id, sent_at DESC) WHERE status = 'sent';
-- 保留期清理
CREATE INDEX plugin_hook_deliveries_created ON plugin_hook_deliveries (created_at);

-- +goose Down
DROP TABLE IF EXISTS plugin_hook_deliveries;
DROP TABLE IF EXISTS plugin_hooks;
