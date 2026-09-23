-- +goose Up

-- 自研的主题与插件体系，三层：
--
--   site_themes          外观层：配色、Logo、站名、文案、自定义 CSS
--   site_slots           内容层：白名单插槽位，后台往里塞受限 HTML
--   plugin_hooks         行为层：插件以出站 webhook 订阅事件
--   plugin_hook_deliveries  出站队列，带重试
--
-- 刻意不做 Xboard 那种「上传 zip 就执行」的插件包：那等于给后台开一个
-- 任意代码执行入口，一个被盗的管理员账号就能拿到整台机器。这里插件跑在
-- 它自己的进程里，面板只往外发签过名的 HTTP 请求 —— 插件崩了、慢了、
-- 被攻破了，面板都还在。

--------------------------------------------------------------------------------
-- 一、主题
--------------------------------------------------------------------------------

CREATE TABLE site_themes (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code        text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_-]{1,38}$'),
  name        text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 60),
  -- 内置预设不允许删除，只能被复制成自定义主题再改
  is_builtin  boolean NOT NULL DEFAULT false,
  is_active   boolean NOT NULL DEFAULT false,

  -- tokens 是一组扁平的键值，键名与 portal 里的 CSS 变量一一对应：
  --   {"brand":"#5b8cff","bg":"#0e1116","radius":"14px", ...}
  -- 扁平而不是嵌套，是为了前端能无脑地 setProperty('--'+k, v)，
  -- 不用为每套主题写一段映射代码。
  tokens      jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- 站名、Logo、登录页标语这类文本与图片。图片存 data URI 或外链。
  branding    jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- 自定义 CSS。管理员输入，注入用户端。
  -- 长度设上限：一段几百 KB 的 CSS 会让每个用户的首屏都变慢，
  -- 而这种事在出问题之前没人会注意到。
  custom_css  text NOT NULL DEFAULT '' CHECK (length(custom_css) <= 65536),

  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);

-- 同一租户最多一套生效主题。用部分唯一索引而不是应用层判断：
-- 「先取消旧的再启用新的」这两步之间如果失败，站点会变成没有主题，
-- 而数据库约束能让整件事要么成要么不成。
CREATE UNIQUE INDEX uq_site_themes_active
  ON site_themes (tenant_id) WHERE is_active;

--------------------------------------------------------------------------------
-- 二、前端插槽
--------------------------------------------------------------------------------

-- slot_key 是白名单，不是自由文本：插槽位置要和前端模板里的挂载点对得上，
-- 允许任意键名只会让后台列出一堆前端根本不会渲染的死插槽。
CREATE TABLE site_slots (
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  slot_key    text NOT NULL CHECK (slot_key IN (
                'portal.login.notice',    -- 登录页提示条
                'portal.home.banner',     -- 概览页顶部横幅
                'portal.home.aside',      -- 概览页右侧附加卡片
                'portal.sidebar.extra',   -- 侧栏底部附加链接
                'portal.subscribe.notice',-- 订阅页使用说明
                'portal.plans.notice',    -- 选购页说明（退换、发票等）
                'portal.footer'           -- 页脚
              )),
  -- 服务端净化后的 HTML。原文不存：只留能渲染的那份，
  -- 避免「库里存着脏的、渲染时才洗」这种每次读都要洗一遍的设计，
  -- 也避免哪天某处忘了洗。
  content     text NOT NULL DEFAULT '' CHECK (length(content) <= 32768),
  enabled     boolean NOT NULL DEFAULT true,
  updated_by  uuid REFERENCES users(id),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, slot_key)
);

--------------------------------------------------------------------------------
-- 三、服务端钩子
--------------------------------------------------------------------------------

CREATE TABLE plugin_hooks (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code          text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_-]{1,38}$'),
  name          text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 80),
  description   text NOT NULL DEFAULT '',
  enabled       boolean NOT NULL DEFAULT false,

  -- 订阅的事件。白名单在应用层校验（事件名会随功能增加，
  -- 放进 CHECK 意味着每加一个事件都要一次迁移）。
  events        text[] NOT NULL DEFAULT '{}',

  endpoint_url  text NOT NULL CHECK (endpoint_url ~ '^https?://'),
  -- HMAC 密钥，信封加密。插件用它验签，确认请求确实来自面板。
  secret_encrypted bytea,

  timeout_ms    integer NOT NULL DEFAULT 5000 CHECK (timeout_ms BETWEEN 500 AND 30000),
  max_attempts  integer NOT NULL DEFAULT 5 CHECK (max_attempts BETWEEN 1 AND 10),

  created_by    uuid REFERENCES users(id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);

-- 出站队列。形状照抄 notification_deliveries —— 那套重试与去重
-- 已经在生产里跑了一段时间，没必要为了「插件」这个名字另发明一遍。
CREATE TABLE plugin_hook_deliveries (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  hook_id       uuid NOT NULL REFERENCES plugin_hooks(id) ON DELETE CASCADE,
  event         text NOT NULL,
  -- 同一事件对同一钩子只投一次。业务侧重复触发（比如支付回调重放）
  -- 不该让插件那边收到两遍。
  dedupe_key    text NOT NULL,
  payload       jsonb NOT NULL,
  status        text NOT NULL DEFAULT 'queued'
                CHECK (status IN ('queued','sent','failed','suppressed')),
  attempts      integer NOT NULL DEFAULT 0,
  max_attempts  integer NOT NULL DEFAULT 5,
  next_retry_at timestamptz,
  response_code integer,
  error_message text NOT NULL DEFAULT '',
  sent_at       timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, hook_id, dedupe_key)
);

CREATE INDEX idx_plugin_hook_deliveries_due
  ON plugin_hook_deliveries (tenant_id, next_retry_at)
  WHERE status = 'queued';
CREATE INDEX idx_plugin_hook_deliveries_hook
  ON plugin_hook_deliveries (tenant_id, hook_id, created_at DESC);

--------------------------------------------------------------------------------
-- RLS 与授权
--------------------------------------------------------------------------------

ALTER TABLE site_themes            ENABLE ROW LEVEL SECURITY;
ALTER TABLE site_slots             ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_hooks           ENABLE ROW LEVEL SECURITY;
ALTER TABLE plugin_hook_deliveries ENABLE ROW LEVEL SECURITY;

CREATE POLICY site_themes_tenant ON site_themes
  USING (tenant_id = app.current_tenant_id());
CREATE POLICY site_slots_tenant ON site_slots
  USING (tenant_id = app.current_tenant_id());
CREATE POLICY plugin_hooks_tenant ON plugin_hooks
  USING (tenant_id = app.current_tenant_id());
CREATE POLICY plugin_hook_deliveries_tenant ON plugin_hook_deliveries
  USING (tenant_id = app.current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON site_themes  TO aegis_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON site_slots   TO aegis_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON plugin_hooks TO aegis_app;
GRANT SELECT, INSERT, UPDATE ON plugin_hook_deliveries TO aegis_app;

--------------------------------------------------------------------------------
-- 权限
--------------------------------------------------------------------------------

-- +goose StatementBegin
INSERT INTO permissions (code, domain, description, high_risk)
VALUES ('platform.appearance.read',  'platform', '查看主题与插槽配置', false),
       ('platform.appearance.write', 'platform', '修改主题、自定义 CSS 与插槽内容', true),
       ('platform.plugin.read',      'platform', '查看插件钩子与投递记录', false),
       ('platform.plugin.write',     'platform', '增删改插件钩子、重发投递', true)
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
  FROM roles r
 CROSS JOIN (VALUES ('platform.appearance.read'), ('platform.appearance.write'),
                    ('platform.plugin.read'),     ('platform.plugin.write')) AS p(code)
 WHERE r.is_system AND r.code IN ('tenant_admin', 'platform_admin')
ON CONFLICT (role_id, permission_code) DO NOTHING;
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 内置主题预设
--------------------------------------------------------------------------------
--
-- 三套，覆盖最常见的三种诉求：跟随现状、更克制、更亮。
-- 键名和 portal 现有的 CSS 变量对齐，所以「不启用任何主题」和
-- 「启用 default」看起来是一样的 —— 这样第一次启用主题不会突然变脸。

-- +goose StatementBegin
INSERT INTO site_themes (tenant_id, code, name, is_builtin, is_active, tokens, branding)
SELECT t.id, v.code, v.name, true, v.code = 'default', v.tokens::jsonb, v.branding::jsonb
  FROM tenants t
 CROSS JOIN (VALUES
   -- default 的取值必须和 portal 里 :root 的现有值逐个一致，
   -- 否则「启用默认主题」会让站点突然换个颜色，而那正是最不该
   -- 发生变化的一次操作。
   ('default', '默认（潘多拉紫）',
    '{"brand":"#6d5efc","brand-2":"#a855f7","brand-soft":"rgba(109,94,252,.14)",'
    '"brand-on-soft":"#9c92ff","r-lg":"14px"}',
    '{"site_name":"潘多拉面板","tagline":"稳定、快速、随处可用"}'),
   ('midnight', '午夜（低饱和）',
    '{"brand":"#7c8aa5","brand-2":"#94a3b8","brand-soft":"rgba(124,138,165,.16)",'
    '"brand-on-soft":"#aab6c8","r-lg":"10px"}',
    '{"site_name":"潘多拉面板","tagline":""}'),
   ('aurora', '极光（青绿）',
    '{"brand":"#0f9d8a","brand-2":"#14b8a6","brand-soft":"rgba(15,157,138,.14)",'
    '"brand-on-soft":"#3ec9b4","r-lg":"18px"}',
    '{"site_name":"潘多拉面板","tagline":""}')
 ) AS v(code, name, tokens, branding)
ON CONFLICT (tenant_id, code) DO NOTHING;
-- +goose StatementEnd

-- 插槽先建空行：后台列表直接读这张表就能列出所有可用位置，
-- 不必在前后端各维护一份插槽清单然后指望它们不走样。
-- +goose StatementBegin
INSERT INTO site_slots (tenant_id, slot_key, content, enabled)
SELECT t.id, k.slot_key, '', false
  FROM tenants t
 CROSS JOIN (VALUES ('portal.login.notice'), ('portal.home.banner'),
                    ('portal.home.aside'), ('portal.sidebar.extra'),
                    ('portal.subscribe.notice'), ('portal.plans.notice'),
                    ('portal.footer')) AS k(slot_key)
ON CONFLICT (tenant_id, slot_key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS plugin_hook_deliveries;
DROP TABLE IF EXISTS plugin_hooks;
DROP TABLE IF EXISTS site_slots;
DROP TABLE IF EXISTS site_themes;
DELETE FROM role_permissions
 WHERE permission_code IN ('platform.appearance.read','platform.appearance.write',
                           'platform.plugin.read','platform.plugin.write');
DELETE FROM permissions
 WHERE code IN ('platform.appearance.read','platform.appearance.write',
                'platform.plugin.read','platform.plugin.write');
