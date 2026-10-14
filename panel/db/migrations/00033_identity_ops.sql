-- +goose Up
-- identity / audit / notify：后台批量生成账号任务、同源 IP 复核结论、审计聚类与按 IP 查询的部分索引、群发邮件外壳模板。
SET LOCAL lock_timeout = '5s';

-- identity 模块：后台批量生成账号任务（属主 identity）。
CREATE TABLE user_generation_jobs (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 提交任务的管理员；不挂外键：任务是操作记录，不挡删账号
  actor_id          uuid NOT NULL,
  status            text NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
  total             int  NOT NULL CHECK (total BETWEEN 1 AND 500),
  completed         int  NOT NULL DEFAULT 0 CHECK (completed >= 0),
  failed            int  NOT NULL DEFAULT 0 CHECK (failed >= 0),
  email_prefix      text NOT NULL CHECK (email_prefix ~ '^[a-z0-9-]{1,20}$'),
  email_domain      text NOT NULL CHECK (
                        email_domain ~ '^[a-z0-9.-]{4,63}$' AND strpos(email_domain, '.') > 0),
  -- 不挂外键：worker 每批写入前自行确认分组仍在，删组不被任务行挡住
  group_id          uuid,
  reason            text NOT NULL CHECK (char_length(reason) BETWEEN 5 AND 500),
  result_encrypted  bytea,
  result_expires_at timestamptz,
  result_purged_at  timestamptz,
  error             text CHECK (char_length(error) <= 500),
  attempts          int  NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  lease_until       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  started_at        timestamptz,
  finished_at       timestamptz,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT user_generation_jobs_progress
    CHECK (completed + failed <= total),
  CONSTRAINT user_generation_jobs_finished
    CHECK ((status IN ('succeeded', 'failed')) = (finished_at IS NOT NULL)),
  CONSTRAINT user_generation_jobs_succeeded_complete
    CHECK (status <> 'succeeded' OR completed = total),
  CONSTRAINT user_generation_jobs_lease
    CHECK ((status = 'running') = (lease_until IS NOT NULL)),
  CONSTRAINT user_generation_jobs_started
    CHECK (status = 'queued' OR started_at IS NOT NULL),
  CONSTRAINT user_generation_jobs_result_expiry
    CHECK (result_encrypted IS NULL OR status IN ('queued', 'running') OR result_expires_at IS NOT NULL),
  CONSTRAINT user_generation_jobs_purged
    CHECK (result_purged_at IS NULL OR result_encrypted IS NULL)
);

CREATE INDEX user_generation_jobs_recent_idx
  ON user_generation_jobs (created_at DESC, id DESC);
CREATE INDEX user_generation_jobs_open_idx
  ON user_generation_jobs (created_at)
  WHERE status IN ('queued', 'running');
CREATE INDEX user_generation_jobs_result_expiry_idx
  ON user_generation_jobs (result_expires_at)
  WHERE result_encrypted IS NOT NULL;
CREATE INDEX user_generation_jobs_finished_idx
  ON user_generation_jobs (finished_at)
  WHERE finished_at IS NOT NULL;

COMMENT ON TABLE user_generation_jobs IS
  '后台批量生成账号任务：进度与可下载结果密文（邮箱与初始口令）。结果密文只保留 24 小时，到期由 reconcile:user_generation 清掉；'
  '任务行在结束 30 天后由 retention:identity 删除。认领用租约；每批的用户、口令与进度、结果密文在同一事务写入，接着做不会重复建号。';

-- audit 模块：每个来源 IP 聚类当前的复核结论（属主 audit）。
CREATE TABLE ip_cluster_reviews (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  ip_hash    bytea NOT NULL CHECK (octet_length(ip_hash) = 32),
  decision   text NOT NULL CHECK (decision IN ('normal', 'disabled')),
  note       text NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
  -- 不挂外键：与 audit_events.actor_id 一样，audit 不依赖业务模块
  decided_by uuid NOT NULL,
  decided_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz,
  CONSTRAINT ip_cluster_reviews_ip_key UNIQUE (ip_hash),
  CONSTRAINT ip_cluster_reviews_expiry
    CHECK ((decision = 'normal') = (expires_at IS NOT NULL))
);

COMMENT ON TABLE ip_cluster_reviews IS
  '同源 IP 聚类不落表，由 audit 模块按近 90 天 audit_events 现算；本表只存每个来源 IP 当前的复核结论，历史在审计 risk.ip_cluster.*。';

-- 聚类列表按近 90 天窗口扫用户事件。本迁移落地时尚无生产部署、audit_events 为空，不用 CONCURRENTLY；
-- lock_timeout 5 秒，拿不到锁整个迁移回滚，重跑即可。
CREATE INDEX audit_events_cluster_idx
  ON audit_events (occurred_at DESC, ip_hash)
  WHERE ip_hash IS NOT NULL AND actor_kind = 'user' AND actor_id IS NOT NULL;

-- 按一个来源 IP 查：聚类成员、画像里同 IP 账号数、访问明细按 IP 筛选、取聚类的 IP 密文。
CREATE INDEX audit_events_ip_idx
  ON audit_events (ip_hash, occurred_at DESC)
  WHERE ip_hash IS NOT NULL;

-- 后台群发邮件外壳模板：主题与正文由管理员每次填写，经变量 subject / body 传入；
-- 与 notify service registry 中 admin.broadcast 的默认 subject/body 逐字一致。
INSERT INTO notification_templates (code, channel, subject, body) VALUES
  ('admin.broadcast', 'email', '{{subject}}', '{{body}}');

-- +goose Down
SET LOCAL lock_timeout = '5s';

DELETE FROM notification_deliveries WHERE template_code = 'admin.broadcast';
DELETE FROM notification_templates WHERE code = 'admin.broadcast';
DROP INDEX IF EXISTS audit_events_ip_idx;
DROP INDEX IF EXISTS audit_events_cluster_idx;
DROP TABLE IF EXISTS ip_cluster_reviews;
DROP TABLE IF EXISTS user_generation_jobs;
