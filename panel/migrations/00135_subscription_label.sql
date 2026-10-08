-- 订阅备注名（购买模型统一 Q7，用户 2026-10-07 定；w7buya）。
--
-- 同一个人可以有多份订阅（给家人另买一份），App 里要分得清：
--   subscriptions.label         用户给这一份起的名字，没起为空。配置名「站点名 · 备注名」由
--                               subscription.ProfileName 拼，会进 Content-Disposition 头，
--                               所以不许有控制字符；规范化在 purchase.NormalizeLabel（1–16 字、
--                               首尾无空白），这里的 CHECK 是数据库兜底。
--   orders.subscription_label   门户「另买一份」付款前就收集名字，履约建订阅时才写进订阅
--                               （billing provisionSubscription），所以先存在新购单上；只许 kind='new'。
-- 同一用户名下不重名的唯一索引在 00136（大表上 CONCURRENTLY，要单独的 NO TRANSACTION 文件）。
--
-- 节点下发纪元（00101）：订阅表原来一条触发器，任何列变化都推进纪元。改名不影响下发名单，
-- 却会让所有节点池重算一遍，所以拆成两条：插入与删除照旧；更新只在「除 label、updated_at 以外
-- 有列变了」时推进。用排除名单而不是白名单：以后新加的、会影响下发的列默认照样推进，不会漏。
-- 引用 OLD 的 WHEN 不能同时挂 INSERT / DELETE，所以是两条。
--
-- 锁与耗时：两张都是大表。加可空、不带默认值的列只改元数据，不重写表；CHECK 写成 NOT VALID
-- 再 VALIDATE（同 00129）。同一个事务里 ADD COLUMN 已拿着 ACCESS EXCLUSIVE，VALIDATE 的那次
-- 顺序扫描也在这把锁下：新列全为空，扫描只读不写，5k 副本（对照机空闲，subscriptions 5000 行）整个 Up 约 40ms（扣除 docker exec 开销），按两倍估不到 0.1s。
-- 换触发器要 SHARE ROW EXCLUSIVE。lock_timeout 5s 拿不到就失败重来，不排在业务事务后面堵表。
--
-- 回滚（Down）会丢掉用户起的备注名：只影响显示，不涉及钱和权益，所以不算 irreversible。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE public.subscriptions ADD COLUMN label text;
ALTER TABLE public.subscriptions
  ADD CONSTRAINT subscriptions_label_check
  CHECK (label IS NULL OR (char_length(label) BETWEEN 1 AND 16
                           AND label !~ '^[[:space:]]' AND label !~ '[[:space:]]$'
                           AND label !~ '[[:cntrl:]]')) NOT VALID;
ALTER TABLE public.subscriptions VALIDATE CONSTRAINT subscriptions_label_check;
COMMENT ON COLUMN public.subscriptions.label IS
  '用户给这一份订阅起的备注名（1–16 字，无控制字符）；同一用户内不区分大小写唯一（00136）';

ALTER TABLE public.orders ADD COLUMN subscription_label text;
ALTER TABLE public.orders
  ADD CONSTRAINT orders_subscription_label_check
  CHECK (subscription_label IS NULL
         OR (kind = 'new' AND char_length(subscription_label) BETWEEN 1 AND 16
             AND subscription_label !~ '^[[:space:]]' AND subscription_label !~ '[[:space:]]$'
             AND subscription_label !~ '[[:cntrl:]]')) NOT VALID;
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_subscription_label_check;
COMMENT ON COLUMN public.orders.subscription_label IS
  '新购单给新的一份起的备注名，履约建订阅时写进 subscriptions.label';
-- orders 是列级 INSERT 授权（00036 / configure-app-role.sql 的白名单），新列要单独放给运行角色
GRANT INSERT (subscription_label) ON public.orders TO aegis_app;

-- +goose StatementBegin
DROP TRIGGER zz_node_delivery_epoch_subscriptions ON public.subscriptions;

-- 订阅的新增与删除：照旧推进
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_subscriptions
  AFTER INSERT OR DELETE ON public.subscriptions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();

-- 订阅的更新：除备注名与 updated_at 之外有列变了才推进（改名不让节点池重算）
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_subscriptions_update
  AFTER UPDATE ON public.subscriptions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN ((to_jsonb(OLD) - 'label' - 'updated_at') IS DISTINCT FROM (to_jsonb(NEW) - 'label' - 'updated_at'))
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 恢复 00101 原样的单条触发器
-- +goose StatementBegin
DROP TRIGGER zz_node_delivery_epoch_subscriptions_update ON public.subscriptions;
DROP TRIGGER zz_node_delivery_epoch_subscriptions ON public.subscriptions;
CREATE CONSTRAINT TRIGGER zz_node_delivery_epoch_subscriptions
  AFTER INSERT OR UPDATE OR DELETE ON subscriptions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_node_delivery_epoch();
-- +goose StatementEnd

REVOKE INSERT (subscription_label) ON public.orders FROM aegis_app;
ALTER TABLE public.orders DROP COLUMN subscription_label;
ALTER TABLE public.subscriptions DROP COLUMN label;
