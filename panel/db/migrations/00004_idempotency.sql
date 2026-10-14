-- +goose Up
-- 幂等键：同一操作者在同一作用域内用同一个 Idempotency-Key，只执行一次业务。
-- 按操作者隔离靠 UNIQUE(scope, actor_id, idempotency_key)；actor_id 不对用户表建外键，本模块不依赖业务模块。
-- 不挂禁删触发器：保留期任务 retention:idempotency 按 created_at 分批删 30 天前的行。
CREATE TABLE idempotency_keys (
  id                        uuid PRIMARY KEY DEFAULT uuidv7(),
  scope                     text NOT NULL CHECK (scope ~ '^[a-z0-9_]{1,64}$'),
  actor_id                  uuid NOT NULL,
  idempotency_key           text NOT NULL
                              CHECK (octet_length(idempotency_key) BETWEEN 1 AND 255
                                     AND idempotency_key !~ '[\x01-\x1f\x7f]'),
  -- 请求指纹：sha256(方法 \n 路径与查询 \n 请求体)
  request_hash              bytea NOT NULL CHECK (octet_length(request_hash) = 32),
  status                    text NOT NULL DEFAULT 'in_flight'
                              CHECK (status IN ('in_flight', 'succeeded', 'failed')),
  -- 认领代数：失败后由同一请求接管时加一，完成时按代数比对，旧持有者写不进来
  generation                bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
  -- 租约：认领或接管时为 now()+5 分钟，完成后清空；过期只是对账信号，不会被接管
  locked_until              timestamptz,
  response_code             integer,
  -- replayable 为真时 response_body 是可原样重放的响应体（可能为空串）
  replayable                boolean NOT NULL DEFAULT false,
  response_body             bytea CHECK (octet_length(response_body) <= 1048576),
  -- 重放白名单里的响应头，缺省为 NULL
  response_content_type     text CHECK (octet_length(response_content_type) <= 255),
  response_location         text CHECK (octet_length(response_location) <= 2048),
  response_etag             text CHECK (octet_length(response_etag) <= 255),
  response_cache_control    text CHECK (octet_length(response_cache_control) <= 512),
  response_content_language text CHECK (octet_length(response_content_language) <= 128),
  -- 业务事务内完成时绑定的业务资源（如订单），只记类型与 id，不建外键
  resource_type             text CHECK (resource_type ~ '^[a-z0-9_]{1,64}$'),
  resource_id               uuid,
  created_at                timestamptz NOT NULL DEFAULT now(),
  completed_at              timestamptz,

  CONSTRAINT idempotency_keys_scope_actor_key UNIQUE (scope, actor_id, idempotency_key),
  CONSTRAINT idempotency_keys_resource_pair CHECK ((resource_type IS NULL) = (resource_id IS NULL)),
  CONSTRAINT idempotency_keys_replay_body CHECK (replayable = (response_body IS NOT NULL)),
  CONSTRAINT idempotency_keys_shape CHECK (
    CASE status
      WHEN 'in_flight' THEN
        locked_until IS NOT NULL AND response_code IS NULL AND completed_at IS NULL
        AND NOT replayable AND resource_type IS NULL
        AND response_content_type IS NULL AND response_location IS NULL AND response_etag IS NULL
        AND response_cache_control IS NULL AND response_content_language IS NULL
      WHEN 'succeeded' THEN
        locked_until IS NULL AND completed_at IS NOT NULL
        AND response_code IS NOT NULL AND response_code BETWEEN 200 AND 299
      WHEN 'failed' THEN
        locked_until IS NULL AND completed_at IS NOT NULL
        AND response_code IS NOT NULL AND response_code BETWEEN 100 AND 999 AND response_code NOT BETWEEN 200 AND 299
        AND NOT replayable AND resource_type IS NULL
        AND response_content_type IS NULL AND response_location IS NULL AND response_etag IS NULL
        AND response_cache_control IS NULL AND response_content_language IS NULL
      ELSE false
    END
  )
);

CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE IF EXISTS idempotency_keys;
