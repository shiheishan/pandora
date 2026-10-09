-- 门户目录、外观与站点设置的缓存纪元（性能总方案第 0 波 C 缓存收拢，w12cache；给 W1-b 的目录、
-- 外观、站点设置缓存与以后的降级开关缓存铺路）。
--
-- 这几样「人人相同」，现在每个请求都重算（套餐目录 3+2P 次往返、外观每次重做 JSON 与正则）。W1-b 把
-- 它们放进进程内缓存（platform/cache），按纪元判有效：命中时零往返，后台一改，提交后通知送达、
-- 下一次请求就重算。三个纪元都走 00155 的 app.bump_cache_epoch('<种类>')：提交时推进 <种类>_epoch
-- 并在 aegis_cache_epoch 上发载荷 '<种类>'。
--
--   - catalog_epoch：套餐（库存的占用与售出计数、updated_at 除外——每笔订单都改它们，目录不显示）、
--     套餐版本、价格、配额定义、流量包。可见时间窗（visible_from / until、valid_from / until）到点
--     生效没有写，读方按条目里最早的边界硬过期。
--   - appearance_epoch：主题与插槽。
--   - site_settings_epoch：租户行（站点名、时区、订阅路径前缀等，写得很少）、系统设置、降级开关。
--
-- 锁：建序列与触发器；触发器对各表拿 SHARE ROW EXCLUSIVE，不扫表，毫秒级。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
CREATE SEQUENCE public.catalog_epoch AS bigint;
CREATE SEQUENCE public.appearance_epoch AS bigint;
CREATE SEQUENCE public.site_settings_epoch AS bigint;
GRANT SELECT ON SEQUENCE public.catalog_epoch, public.appearance_epoch, public.site_settings_epoch TO aegis_app;
COMMENT ON SEQUENCE public.catalog_epoch IS '门户目录纪元：套餐、套餐版本、价格、配额定义、流量包变化时推进（00156）。';
COMMENT ON SEQUENCE public.appearance_epoch IS '外观纪元：主题与插槽变化时推进（00156）。';
COMMENT ON SEQUENCE public.site_settings_epoch IS '站点设置纪元：租户行、系统设置、降级开关变化时推进（00156）。';

-- 目录：套餐增删
CREATE CONSTRAINT TRIGGER zz_catalog_epoch_plans_rows
  AFTER INSERT OR DELETE ON public.plans
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('catalog');

-- 目录：套餐改动；下单占库存、付款转售出只改两个计数（和 updated_at），不推进
CREATE CONSTRAINT TRIGGER zz_catalog_epoch_plans_update
  AFTER UPDATE ON public.plans
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN ((to_jsonb(OLD) - 'stock_reserved' - 'stock_sold' - 'updated_at')
        IS DISTINCT FROM (to_jsonb(NEW) - 'stock_reserved' - 'stock_sold' - 'updated_at'))
  EXECUTE FUNCTION app.bump_cache_epoch('catalog');

CREATE CONSTRAINT TRIGGER zz_catalog_epoch_plan_versions
  AFTER INSERT OR UPDATE OR DELETE ON public.plan_versions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('catalog');

CREATE CONSTRAINT TRIGGER zz_catalog_epoch_prices
  AFTER INSERT OR UPDATE OR DELETE ON public.prices
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('catalog');

CREATE CONSTRAINT TRIGGER zz_catalog_epoch_quota_definitions
  AFTER INSERT OR UPDATE OR DELETE ON public.quota_definitions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('catalog');

CREATE CONSTRAINT TRIGGER zz_catalog_epoch_traffic_packs
  AFTER INSERT OR UPDATE OR DELETE ON public.traffic_packs
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('catalog');

-- 外观
CREATE CONSTRAINT TRIGGER zz_appearance_epoch_site_themes
  AFTER INSERT OR UPDATE OR DELETE ON public.site_themes
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('appearance');

CREATE CONSTRAINT TRIGGER zz_appearance_epoch_site_slots
  AFTER INSERT OR UPDATE OR DELETE ON public.site_slots
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('appearance');

-- 站点设置：租户行（真变了才推进；updated_at 由 BEFORE 触发器每次都改，不算）
CREATE CONSTRAINT TRIGGER zz_site_settings_epoch_tenants_rows
  AFTER INSERT OR DELETE ON public.tenants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('site_settings');

CREATE CONSTRAINT TRIGGER zz_site_settings_epoch_tenants_update
  AFTER UPDATE ON public.tenants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN ((to_jsonb(OLD) - 'updated_at') IS DISTINCT FROM (to_jsonb(NEW) - 'updated_at'))
  EXECUTE FUNCTION app.bump_cache_epoch('site_settings');

CREATE CONSTRAINT TRIGGER zz_site_settings_epoch_system_settings
  AFTER INSERT OR UPDATE OR DELETE ON public.system_settings
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('site_settings');

CREATE CONSTRAINT TRIGGER zz_site_settings_epoch_feature_switches
  AFTER INSERT OR UPDATE OR DELETE ON public.feature_switches
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('site_settings');
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DROP TRIGGER IF EXISTS zz_site_settings_epoch_feature_switches ON public.feature_switches;
DROP TRIGGER IF EXISTS zz_site_settings_epoch_system_settings ON public.system_settings;
DROP TRIGGER IF EXISTS zz_site_settings_epoch_tenants_update ON public.tenants;
DROP TRIGGER IF EXISTS zz_site_settings_epoch_tenants_rows ON public.tenants;
DROP TRIGGER IF EXISTS zz_appearance_epoch_site_slots ON public.site_slots;
DROP TRIGGER IF EXISTS zz_appearance_epoch_site_themes ON public.site_themes;
DROP TRIGGER IF EXISTS zz_catalog_epoch_traffic_packs ON public.traffic_packs;
DROP TRIGGER IF EXISTS zz_catalog_epoch_quota_definitions ON public.quota_definitions;
DROP TRIGGER IF EXISTS zz_catalog_epoch_prices ON public.prices;
DROP TRIGGER IF EXISTS zz_catalog_epoch_plan_versions ON public.plan_versions;
DROP TRIGGER IF EXISTS zz_catalog_epoch_plans_update ON public.plans;
DROP TRIGGER IF EXISTS zz_catalog_epoch_plans_rows ON public.plans;
DROP SEQUENCE IF EXISTS public.site_settings_epoch;
DROP SEQUENCE IF EXISTS public.appearance_epoch;
DROP SEQUENCE IF EXISTS public.catalog_epoch;
-- +goose StatementEnd
