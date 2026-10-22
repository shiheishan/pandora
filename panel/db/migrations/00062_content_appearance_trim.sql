-- +goose Up
-- 内容与外观对齐：删掉后台能配、用户端不生效的东西（用户 2026-10-21 定，后台与用户端对齐核对 2.1-3、2.1-4、2.2-4、2.2-5、3.4）。
-- 动 content（content_pages、content_page_feedback）与 appearance（site_slots、site_themes）两个模块的表：
-- 这一轮只分到 00062 一个号，两块是同一件事，放在一起；各表照旧只由属主模块的 repo 读写。
--   1. 知识库只做中文：content_pages 删 locale、target_platforms、min_client_version、max_client_version。
--      门户一直按 locale=zh-CN、platform=any、不带客户端版本读：语言不是 zh-CN 的、设了客户端版本区间的已发布版本，
--      用户端本来就看不到，删列前先归档，免得删列后变成可见；之后「同一受众」只剩限定套餐与可见性，
--      同一受众里有几个已发布版本的只留版本号最大的那个（门户本来也只展示它），其余归档。用户端看到的文章不变。
--   2. 删「这篇有帮助吗」：content_page_feedback 整表删（只写不读）。
--   3. 删插槽 portal.sidebar.extra、portal.footer（用户端没有挂载点）：删这两行，收窄 slot_key 的 CHECK
--      （与 appearance service 的 SlotCatalog、门户挂载点三处一致，守卫 TestSlotCatalogMatchesMigration 读本文件）。
--   4. 删主题 Logo（后台没有上传入口）：site_themes.branding 去掉 logo 键。
-- 删列一步做、不分「先停用再删」两步：安装器升级时先停服务再迁移（install.sh），迁移失败不起服务，
-- 没有新旧二进制同时连库的窗口；现在也没有生产部署（10-09 确认）。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：content_pages、content_page_feedback、site_slots、site_themes 各拿
--   ACCESS EXCLUSIVE；删列只改系统目录、不重写表，UPDATE / DELETE 与行数成正比，这几张表都是后台手工维护的几十到几百行，
--   毫秒级。期间挡住的只有知识库、插槽、主题的读写。
--   预检：SELECT count(*) FROM content_pages;
--         SELECT count(*) FROM content_pages WHERE status = 'published'
--            AND (locale <> 'zh-CN' OR min_client_version <> '' OR max_client_version <> '');
--         SELECT count(*) FROM site_slots WHERE slot_key IN ('portal.sidebar.extra', 'portal.footer');
--         SELECT count(*) FROM site_themes WHERE branding ? 'logo';
--     后三个是会被归档或删掉的行数，非零时先告诉管理员这些内容迁移后就没有了（Down 也还原不了，见下）。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 1a. 用户端本来就看不到的已发布版本先归档（与后台归档同口径：只改状态与 updated_at，正文与版本号不动）
UPDATE content_pages SET status = 'archived', updated_at = now()
 WHERE status = 'published'
   AND (locale <> 'zh-CN' OR min_client_version <> '' OR max_client_version <> '');

-- 1b. 新的同一受众（slug、限定套餐、可见性）里只留版本号最大的已发布版本
UPDATE content_pages cp SET status = 'archived', updated_at = now()
 WHERE cp.status = 'published'
   AND EXISTS (
     SELECT 1 FROM content_pages n
      WHERE n.slug = cp.slug AND n.status = 'published' AND n.version > cp.version
        AND n.target_plan_ids = cp.target_plan_ids AND n.visibility = cp.visibility);

-- 1c. 删列（列上的 CHECK 随列一起删）
ALTER TABLE content_pages
  DROP COLUMN locale,
  DROP COLUMN target_platforms,
  DROP COLUMN min_client_version,
  DROP COLUMN max_client_version;

-- 2. 反馈表（外键与索引随表删）
DROP TABLE content_page_feedback;

-- 3. 插槽：先删两行再收窄 CHECK
DELETE FROM site_slots WHERE slot_key IN ('portal.sidebar.extra', 'portal.footer');
ALTER TABLE site_slots DROP CONSTRAINT site_slots_slot_key_check;
ALTER TABLE site_slots ADD CONSTRAINT site_slots_slot_key_check CHECK (slot_key IN (
  'portal.login.notice',
  'portal.home.banner',
  'portal.home.aside',
  'portal.subscribe.notice',
  'portal.plans.notice'
));

-- 4. 主题 Logo
UPDATE site_themes SET branding = branding - 'logo', updated_at = now() WHERE branding ? 'logo';

-- +goose Down
-- 结构退回 00061 之后的样子（列、约束名与内容、反馈表与索引逐项照 00028、00029 原样）；删掉的数据还原不了：
--   - 本迁移归档的知识库版本不会改回已发布（归档后分不清哪些是本迁移改的）；语言一律回填 zh-CN、平台空、客户端版本空，
--     正是门户一直以来的读取口径；
--   - 反馈表建回空表（原来的反馈没有留存）；
--   - 删掉的两个插槽的内容、主题里的 Logo 没有留存，回不来。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE site_slots DROP CONSTRAINT site_slots_slot_key_check;
ALTER TABLE site_slots ADD CONSTRAINT site_slots_slot_key_check CHECK (slot_key IN (
  'portal.login.notice',
  'portal.home.banner',
  'portal.home.aside',
  'portal.sidebar.extra',
  'portal.subscribe.notice',
  'portal.plans.notice',
  'portal.footer'
));

ALTER TABLE content_pages
  ADD COLUMN locale text NOT NULL DEFAULT 'zh-CN' CHECK (locale ~ '^[a-z]{2,3}(-[A-Z]{2})?$'),
  ADD COLUMN target_platforms jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (
    jsonb_typeof(target_platforms) = 'array'
    AND target_platforms <@ '["web","windows","macos","linux","android","ios"]'::jsonb
  ),
  ADD COLUMN min_client_version text NOT NULL DEFAULT '' CHECK (min_client_version = '' OR min_client_version ~ '^v?[0-9]+(\.[0-9]+){0,2}$'),
  ADD COLUMN max_client_version text NOT NULL DEFAULT '' CHECK (max_client_version = '' OR max_client_version ~ '^v?[0-9]+(\.[0-9]+){0,2}$');
-- 00028 里 locale 没有缺省值：回填完去掉
ALTER TABLE content_pages ALTER COLUMN locale DROP DEFAULT;

CREATE TABLE content_page_feedback (
  page_slug    text NOT NULL,
  page_version int NOT NULL,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  helpful      boolean NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (page_slug, page_version, user_id),
  CONSTRAINT content_page_feedback_page_fkey FOREIGN KEY (page_slug, page_version)
    REFERENCES content_pages (slug, version) ON DELETE CASCADE
);
CREATE INDEX content_page_feedback_user_idx ON content_page_feedback (user_id);
