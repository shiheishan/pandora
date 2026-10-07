-- 批量生成账号改成后台任务（用户 2026-10-07 定，方案 A；w5account）。
--
-- 原来 POST v1/users/bulk/generate 在请求里逐个算 Argon2（500 个十几秒），算的时候占着全局
-- 哈希名额，和登录抢；请求一断，管理员连已生成的口令都拿不到。现在请求只登记一个任务，
-- 由 aegis-admin 的后台 worker 逐个生成：一次只占 1 个 Argon2 名额，不挡登录；进度写在这
-- 张表里，界面轮询；完成后结果（邮箱 + 初始口令）可下载。
--
-- 结果里有明文初始口令，所以：
--   - 只存信封加密的密文（AAD 绑定任务 id），库里与备份里都没有明文；
--   - 只保留 24 小时：result_expires_at 一到，worker 把密文清掉（result_purged_at 记时间），
--     任务行本身留着当记录（谁、什么时候、生成了多少，审计里另有一份）；
--   - 下载要权限 + 近期重认证，并且每次下载都写审计。
--
-- 认领用租约：worker 以 FOR UPDATE SKIP LOCKED 认领 queued 或租约已过期的 running 任务，
-- 每批生成后续租；进程中途挂了，租约一过别的实例（或重启后的自己）从 completed 处接着做。
-- 每批的用户、口令与进度、结果密文在同一个事务里写，所以「已建出的账号」与「结果里的
-- 口令」永远一致，接着做不会重复建号。

-- +goose Up
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
CREATE TABLE user_generation_jobs (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 提交任务的管理员。不挂外键：任务是操作记录，不该挡住删账号
  actor_id          uuid NOT NULL,
  status            text NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
  total             int  NOT NULL CHECK (total BETWEEN 1 AND 500),
  completed         int  NOT NULL DEFAULT 0 CHECK (completed >= 0),
  failed            int  NOT NULL DEFAULT 0 CHECK (failed >= 0),
  email_prefix      text NOT NULL CHECK (email_prefix ~ '^[a-z0-9-]{1,20}$'),
  email_domain      text NOT NULL CHECK (char_length(email_domain) BETWEEN 4 AND 63),
  group_id          uuid,
  reason            text NOT NULL CHECK (char_length(reason) BETWEEN 5 AND 500),
  -- 已生成账号的邮箱与初始口令（JSON 数组），信封加密，AAD 绑定任务 id
  result_encrypted  bytea,
  result_expires_at timestamptz,
  result_purged_at  timestamptz,
  error             text,
  attempts          int  NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  lease_until       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  started_at        timestamptz,
  finished_at       timestamptz,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT user_generation_jobs_progress CHECK (completed + failed <= total),
  CONSTRAINT user_generation_jobs_finished CHECK (
    (status IN ('succeeded', 'failed')) = (finished_at IS NOT NULL)),
  CONSTRAINT user_generation_jobs_succeeded_complete CHECK (
    status <> 'succeeded' OR completed = total)
);
-- +goose StatementEnd

-- 后台列表与按管理员查最近的任务
CREATE INDEX idx_user_generation_jobs_recent
  ON user_generation_jobs (tenant_id, created_at DESC);
-- worker 认领：只有没做完的任务进这条索引，它永远很小
CREATE INDEX idx_user_generation_jobs_open
  ON user_generation_jobs (tenant_id, created_at)
  WHERE status IN ('queued', 'running');
-- 到期清结果：只看还留着密文的行
CREATE INDEX idx_user_generation_jobs_result_expiry
  ON user_generation_jobs (tenant_id, result_expires_at)
  WHERE result_encrypted IS NOT NULL;

SELECT app.enable_tenant_rls('user_generation_jobs');

-- 应用登记、认领、写进度、清结果；任务行留作记录，不删不清表
GRANT SELECT, INSERT, UPDATE ON user_generation_jobs TO aegis_app;
REVOKE DELETE, TRUNCATE ON user_generation_jobs FROM aegis_app;

COMMENT ON TABLE user_generation_jobs IS
  '后台批量生成账号的任务：进度与 24 小时内可下载的结果密文（含初始口令），由 aegis-admin 的 worker 逐个生成。';

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- 任务表只是生成过程的载体：建出的账号都在 users 里，任务行删掉不丢账号。
-- 结果密文本来就只留 24 小时。
DROP TABLE IF EXISTS user_generation_jobs;
