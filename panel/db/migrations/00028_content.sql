-- +goose Up
-- content 模块：版本化的知识库与自定义页面（content_pages），以及门户「这篇有帮助吗」的反馈（content_page_feedback）。
-- 依赖 00005_identity.sql 的 users。
--
-- 每次保存都是新的一行（同一 slug 的版本号递增），旧版本只会被改成 archived，正文不改。
-- 正文只当纯文本 / Markdown 源存储与下发，不进可信 HTML 边界（sanitizer_version 记 plain-v1）。
-- 目标平台与限定套餐是排好序、去重的 jsonb 字符串数组：同一受众的判断直接比 jsonb 相等；
-- 限定套餐不建外键（数组元素无法引用），写入前由 content service 经 catalog service 校验存在。

CREATE TABLE content_pages (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  slug               text NOT NULL CHECK (char_length(slug) <= 80 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
  kind               text NOT NULL CHECK (kind IN ('page', 'kb_article', 'tutorial', 'legal')),
  category           text NOT NULL DEFAULT '' CHECK (char_length(category) <= 80),
  version            int NOT NULL CHECK (version >= 1),
  title              text NOT NULL CHECK (char_length(title) BETWEEN 2 AND 160),
  summary            text NOT NULL DEFAULT '' CHECK (char_length(summary) <= 500),
  body               text NOT NULL CHECK (char_length(body) BETWEEN 10 AND 100000),
  locale             text NOT NULL CHECK (locale ~ '^[a-z]{2,3}(-[A-Z]{2})?$'),
  sanitizer_version  text NOT NULL DEFAULT 'plain-v1',
  target_platforms   jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_platforms) = 'array'),
  min_client_version text NOT NULL DEFAULT '' CHECK (min_client_version = '' OR min_client_version ~ '^v?[0-9]+(\.[0-9]+){0,2}$'),
  max_client_version text NOT NULL DEFAULT '' CHECK (max_client_version = '' OR max_client_version ~ '^v?[0-9]+(\.[0-9]+){0,2}$'),
  target_plan_ids    jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(target_plan_ids) = 'array'),
  -- authenticated：登录用户可见；internal：只在后台
  visibility         text NOT NULL CHECK (visibility IN ('authenticated', 'internal')),
  status             text NOT NULL CHECK (status IN ('draft', 'published', 'archived')),
  review_due_at      timestamptz,
  published_at       timestamptz,
  created_by         uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT content_pages_slug_version_key UNIQUE (slug, version),
  -- 发布过的版本必有发布时间（归档的版本可能发布过、也可能只是草稿）
  CONSTRAINT content_pages_published_has_time CHECK (status <> 'published' OR published_at IS NOT NULL)
);

-- 门户读取：只看已发布的
CREATE INDEX content_pages_published_idx ON content_pages (kind, slug, version DESC) WHERE status = 'published';
-- users 外键的 SET NULL 检查
CREATE INDEX content_pages_created_by_idx ON content_pages (created_by) WHERE created_by IS NOT NULL;

-- 按（文章、版本、用户）一行：同一个人对同一版改主意就覆盖，不累计；文章出了新版本，新版从零开始。
-- 外键指向 (slug, version)：反馈的版本必须真实存在。
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

-- users 外键的 CASCADE 检查
CREATE INDEX content_page_feedback_user_idx ON content_page_feedback (user_id);

-- +goose Down
DROP TABLE IF EXISTS content_page_feedback;
DROP TABLE IF EXISTS content_pages;
