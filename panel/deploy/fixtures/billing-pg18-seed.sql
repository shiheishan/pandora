-- billing 域 PG18 测试的共用 fixture。
--
-- 原本内嵌在 deploy/test-checkout-atomic-00039-pg18.sh 的 heredoc 里，只有那个
-- runner 用得到。抽成文件是为了让 deploy/run-pg18-gates.sh 也能灌同一份——
-- 两处各维护一份 seed，迟早会像 orders 的列级授权清单那样，改了一处忘了另一处。
--
-- 依赖：完整迁移栈 + configure-app-role.sql 建立的角色与列级授权。
INSERT INTO tenants(id,slug,display_name,default_currency)
VALUES('93000000-0000-7000-8000-000000000001','checkout39','Checkout 39','CNY');
INSERT INTO users(id,tenant_id,email,display_name,status)
VALUES('93000000-0000-7000-8000-000000000011',
       '93000000-0000-7000-8000-000000000001','checkout39@example.test','Checkout 39','active');
INSERT INTO products(id,tenant_id,code,name,status) VALUES
 ('93000000-0000-7000-8000-000000000021','93000000-0000-7000-8000-000000000001','unlimited','Unlimited','active'),
 ('93000000-0000-7000-8000-000000000022','93000000-0000-7000-8000-000000000001','limited','Limited','active');
INSERT INTO plans(id,tenant_id,product_id,code,name,status,stock_total,purchase_limit_per_user) VALUES
 ('93000000-0000-7000-8000-000000000031','93000000-0000-7000-8000-000000000001',
  '93000000-0000-7000-8000-000000000021','unlimited','Unlimited','draft',NULL,NULL),
 ('93000000-0000-7000-8000-000000000032','93000000-0000-7000-8000-000000000001',
  '93000000-0000-7000-8000-000000000022','limited','Limited','draft',1,1);
INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES
 ('93000000-0000-7000-8000-000000000051','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000031',1),
 ('93000000-0000-7000-8000-000000000052','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000032',1);
UPDATE plan_versions SET frozen_at=now() WHERE id IN
 ('93000000-0000-7000-8000-000000000051','93000000-0000-7000-8000-000000000052');
UPDATE plans SET current_version_id=CASE id
 WHEN '93000000-0000-7000-8000-000000000031'::uuid THEN '93000000-0000-7000-8000-000000000051'::uuid
 ELSE '93000000-0000-7000-8000-000000000052'::uuid END, status='active';
INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,status) VALUES
 ('93000000-0000-7000-8000-000000000041','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000021','CNY',1000,'month',1,'active'),
 ('93000000-0000-7000-8000-000000000042','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000022','CNY',1000,'month',1,'active');
INSERT INTO coupons(id,tenant_id,code,name,discount_type,discount_value,currency,
                    max_redemptions,max_redemptions_per_user,status)
VALUES('93000000-0000-7000-8000-000000000061','93000000-0000-7000-8000-000000000001',
       'FULL100','Full 100','fixed',100,'CNY',1,1,'active');

INSERT INTO ledger_accounts(id,tenant_id,account_type,normal_balance,currency,owner_user_id) VALUES
 ('93000000-0000-7000-8000-000000000071','93000000-0000-7000-8000-000000000001','user_balance','credit','CNY','93000000-0000-7000-8000-000000000011');
INSERT INTO ledger_accounts(id,tenant_id,account_type,normal_balance,currency,owner_ref) VALUES
 ('93000000-0000-7000-8000-000000000072','93000000-0000-7000-8000-000000000001','suspense','debit','CNY','checkout-fixture');
INSERT INTO ledger_transactions(id,tenant_id,kind,currency,source_type,source_id,memo,actor_kind) VALUES
 ('93000000-0000-7000-8000-000000000073','93000000-0000-7000-8000-000000000001','balance_adjusted','CNY','user',
  '93000000-0000-7000-8000-000000000011','checkout fixture','system');
INSERT INTO ledger_entries(tenant_id,transaction_id,account_id,direction,amount,currency,description) VALUES
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000073','93000000-0000-7000-8000-000000000072','debit',900,'CNY','fixture'),
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000073','93000000-0000-7000-8000-000000000071','credit',900,'CNY','fixture');

INSERT INTO idempotency_keys
 (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
 ('93000000-0000-7000-8000-000000000101','93000000-0000-7000-8000-000000000001',
  app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000011'),
  'checkout-a',digest('checkout-a-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000011','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000102','93000000-0000-7000-8000-000000000001',
  app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000011'),
  'checkout-b',digest('checkout-b-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000011','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000103','93000000-0000-7000-8000-000000000001',
 app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000011'),
  'checkout-c',digest('checkout-c-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000011','2099-07-30 20:00:00+00');

-- Settlement fixture.  Every runtime call below is still made through the
-- real TCP aegis_app login; postgres is used only to prepare immutable test
-- state before the Go container starts.
INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
 ('93000000-0000-7000-8000-000000000012','93000000-0000-7000-8000-000000000001',
  'settlement39@example.test','Settlement 39','active'),
 ('93000000-0000-7000-8000-000000000013','93000000-0000-7000-8000-000000000001',
  'settlement-referrer39@example.test','Settlement Referrer 39','active'),
 ('93000000-0000-7000-8000-000000000014','93000000-0000-7000-8000-000000000001',
  'settlement-commission39@example.test','Settlement Commission Buyer 39','active');

INSERT INTO referrals(tenant_id,referee_user_id,referrer_user_id,channel,campaign)
VALUES('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000014',
       '93000000-0000-7000-8000-000000000013','pg18','settlement-gate');
INSERT INTO system_settings (tenant_id,key,value,value_schema) VALUES
 ('93000000-0000-7000-8000-000000000001','commission.rate_percent','10'::jsonb,
  '{"type":"integer","minimum":0,"maximum":50}'::jsonb),
 ('93000000-0000-7000-8000-000000000001','commission.freeze_days','1'::jsonb,
  '{"type":"integer","minimum":0,"maximum":90}'::jsonb),
 ('93000000-0000-7000-8000-000000000001','commission.min_withdraw','1000'::jsonb,
  '{"type":"integer","minimum":0}'::jsonb)
ON CONFLICT (tenant_id,key) DO UPDATE SET value=excluded.value;

INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled)
VALUES('93000000-0000-7000-8000-000000000081',
       '93000000-0000-7000-8000-000000000001',
       'settlement-demo','demo_hmac','Settlement Demo',true);

INSERT INTO tenants(id,slug,display_name,default_currency)
VALUES('93000000-0000-7000-8000-000000000002',
       'checkout39-shadow','Checkout 39 Shadow','CNY');
INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled)
VALUES('93000000-0000-7000-8000-000000000082',
       '93000000-0000-7000-8000-000000000002',
       'settlement-shadow','demo_hmac','RLS Hidden Settlement Provider',true);

INSERT INTO products(id,tenant_id,code,name,status) VALUES
 ('93000000-0000-7000-8000-000000000023','93000000-0000-7000-8000-000000000001','settle-ext','Settle External','active'),
 ('93000000-0000-7000-8000-000000000024','93000000-0000-7000-8000-000000000001','settle-mix','Settle Mixed','active'),
 ('93000000-0000-7000-8000-000000000025','93000000-0000-7000-8000-000000000001','settle-fault-ledger','Settle Fault Ledger','active'),
 ('93000000-0000-7000-8000-000000000026','93000000-0000-7000-8000-000000000001','settle-fault-capture','Settle Fault Capture','active'),
 ('93000000-0000-7000-8000-000000000027','93000000-0000-7000-8000-000000000001','settle-fault-fulfil','Settle Fault Fulfil','active'),
 ('93000000-0000-7000-8000-000000000028','93000000-0000-7000-8000-000000000001','settle-fault-audit','Settle Fault Audit','active'),
 ('93000000-0000-7000-8000-000000000029','93000000-0000-7000-8000-000000000001','settle-corrupt','Settle Corrupt Fixture','active');
INSERT INTO plans(id,tenant_id,product_id,code,name,status,stock_total,purchase_limit_per_user) VALUES
 ('93000000-0000-7000-8000-000000000033','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000023','settle-ext','Settle External','draft',NULL,NULL),
 ('93000000-0000-7000-8000-000000000034','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000024','settle-mix','Settle Mixed','draft',2,1),
 ('93000000-0000-7000-8000-000000000035','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000025','settle-fault-ledger','Settle Fault Ledger','draft',2,NULL),
 ('93000000-0000-7000-8000-000000000036','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000026','settle-fault-capture','Settle Fault Capture','draft',2,NULL),
 ('93000000-0000-7000-8000-000000000037','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000027','settle-fault-fulfil','Settle Fault Fulfil','draft',2,NULL),
 ('93000000-0000-7000-8000-000000000038','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000028','settle-fault-audit','Settle Fault Audit','draft',2,NULL),
 ('93000000-0000-7000-8000-000000000039','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000029','settle-corrupt','Settle Corrupt Fixture','draft',2,NULL);
INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES
 ('93000000-0000-7000-8000-000000000053','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000033',1),
 ('93000000-0000-7000-8000-000000000054','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000034',1),
 ('93000000-0000-7000-8000-000000000055','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000035',1),
 ('93000000-0000-7000-8000-000000000056','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000036',1),
 ('93000000-0000-7000-8000-000000000057','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000037',1),
 ('93000000-0000-7000-8000-000000000058','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000038',1),
 ('93000000-0000-7000-8000-000000000059','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000039',1);
UPDATE plan_versions SET frozen_at=now() WHERE id IN
 ('93000000-0000-7000-8000-000000000053','93000000-0000-7000-8000-000000000054','93000000-0000-7000-8000-000000000055',
  '93000000-0000-7000-8000-000000000056','93000000-0000-7000-8000-000000000057','93000000-0000-7000-8000-000000000058',
  '93000000-0000-7000-8000-000000000059');
UPDATE plans SET current_version_id=CASE id
 WHEN '93000000-0000-7000-8000-000000000033'::uuid THEN '93000000-0000-7000-8000-000000000053'::uuid
 WHEN '93000000-0000-7000-8000-000000000034'::uuid THEN '93000000-0000-7000-8000-000000000054'::uuid
 WHEN '93000000-0000-7000-8000-000000000035'::uuid THEN '93000000-0000-7000-8000-000000000055'::uuid
 WHEN '93000000-0000-7000-8000-000000000036'::uuid THEN '93000000-0000-7000-8000-000000000056'::uuid
 WHEN '93000000-0000-7000-8000-000000000037'::uuid THEN '93000000-0000-7000-8000-000000000057'::uuid
 WHEN '93000000-0000-7000-8000-000000000038'::uuid THEN '93000000-0000-7000-8000-000000000058'::uuid
 ELSE '93000000-0000-7000-8000-000000000059'::uuid END, status='active'
 WHERE id IN ('93000000-0000-7000-8000-000000000033','93000000-0000-7000-8000-000000000034',
              '93000000-0000-7000-8000-000000000035','93000000-0000-7000-8000-000000000036',
              '93000000-0000-7000-8000-000000000037','93000000-0000-7000-8000-000000000038',
              '93000000-0000-7000-8000-000000000039');
INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,status) VALUES
 ('93000000-0000-7000-8000-000000000043','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000023','CNY',1000,'month',1,'active'),
 ('93000000-0000-7000-8000-000000000044','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000024','CNY',2000,'month',1,'active'),
 ('93000000-0000-7000-8000-000000000045','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000025','CNY',1000,'month',1,'active'),
 ('93000000-0000-7000-8000-000000000046','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000026','CNY',1000,'month',1,'active'),
 ('93000000-0000-7000-8000-000000000047','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000027','CNY',1000,'month',1,'active'),
 ('93000000-0000-7000-8000-000000000048','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000028','CNY',1000,'month',1,'active'),
 ('93000000-0000-7000-8000-000000000049','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000029','CNY',1000,'month',1,'active');

INSERT INTO coupons(id,tenant_id,code,name,discount_type,discount_value,currency,
                    max_redemptions,max_redemptions_per_user,status)
VALUES('93000000-0000-7000-8000-000000000062','93000000-0000-7000-8000-000000000001',
       'MIX300','Mixed 300','fixed',300,'CNY',1,1,'active');

INSERT INTO ledger_accounts(id,tenant_id,account_type,normal_balance,currency,owner_user_id)
VALUES('93000000-0000-7000-8000-000000000074','93000000-0000-7000-8000-000000000001',
       'user_balance','credit','CNY','93000000-0000-7000-8000-000000000012');
INSERT INTO ledger_accounts(id,tenant_id,account_type,normal_balance,currency,owner_ref)
VALUES('93000000-0000-7000-8000-000000000075','93000000-0000-7000-8000-000000000001',
       'suspense','debit','CNY','settlement-fixture');
-- 佣金计提要借记的平台收入账户。
--
-- 00040 的 assert_commission_entry 要求佣金分录背后有一条借记
-- platform_revenue 的分录，且账户的 owner_ref='main'、normal_balance='credit'、
-- 币种与佣金一致。这个 fixture 里原来只有 suspense 账户，于是任何想预置
-- 一条合法佣金记录的测试都无从下手。
INSERT INTO ledger_accounts(id,tenant_id,account_type,normal_balance,currency,owner_ref)
VALUES('93000000-0000-7000-8000-000000000078','93000000-0000-7000-8000-000000000001',
       'platform_revenue','credit','CNY','main');
INSERT INTO ledger_transactions(id,tenant_id,kind,currency,source_type,source_id,memo,actor_kind)
VALUES('93000000-0000-7000-8000-000000000076','93000000-0000-7000-8000-000000000001',
       'balance_adjusted','CNY','user','93000000-0000-7000-8000-000000000012',
       'settlement fixture','system');
INSERT INTO ledger_entries(tenant_id,transaction_id,account_id,direction,amount,currency,description) VALUES
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000076','93000000-0000-7000-8000-000000000075','debit',2000,'CNY','fixture'),
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000076','93000000-0000-7000-8000-000000000074','credit',2000,'CNY','fixture');

INSERT INTO idempotency_keys
 (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
 ('93000000-0000-7000-8000-000000000301','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-ext',digest('settle-ext-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000302','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-mix',digest('settle-mix-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000303','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('balance_topup_create','93000000-0000-7000-8000-000000000012'),'settle-topup',digest('settle-topup-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000304','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-fault-ledger',digest('settle-fault-ledger-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000305','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('balance_topup_create','93000000-0000-7000-8000-000000000012'),'settle-cancelled',digest('settle-cancelled-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000306','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('balance_topup_create','93000000-0000-7000-8000-000000000012'),'settle-expired',digest('settle-expired-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00');

INSERT INTO idempotency_keys
 (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
 ('93000000-0000-7000-8000-000000000307','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-race',digest('settle-race-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000308','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000014'),'settle-commission-a',digest('settle-commission-a-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000014','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000309','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000014'),'settle-commission-b',digest('settle-commission-b-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000014','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000310','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000014'),'settle-commission-conflict',digest('settle-commission-conflict-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000014','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000311','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-wrong-amount',digest('settle-wrong-amount-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000312','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-wrong-currency',digest('settle-wrong-currency-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000313','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-fault-capture',digest('settle-fault-capture-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000314','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-fault-fulfil',digest('settle-fault-fulfil-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00'),
 ('93000000-0000-7000-8000-000000000315','93000000-0000-7000-8000-000000000001',app.idempotency_actor_scope('order_create','93000000-0000-7000-8000-000000000012'),'settle-fault-audit',digest('settle-fault-audit-request','sha256'),'in_flight','93000000-0000-7000-8000-000000000012','2099-07-30 20:00:00+00');

-- This intentionally legacy renewal has no idempotency_key_id because its
-- insert bypasses the current business-request trigger. Settlement must reject
-- it before money, reservation or subscription state can move. The Go gate
-- separately creates and settles a current actor-bound renewal successfully.
INSERT INTO subscriptions
 (id,tenant_id,user_id,plan_id,plan_version_id,status,current_period_start,
  current_period_end,price_id,snapshot_currency,snapshot_amount)
VALUES
 ('93000000-0000-7000-8000-000000000480','93000000-0000-7000-8000-000000000001',
  '93000000-0000-7000-8000-000000000012','93000000-0000-7000-8000-000000000033',
  '93000000-0000-7000-8000-000000000053','active',now(),now()+interval '1 month',
  '93000000-0000-7000-8000-000000000043','CNY',1000);
ALTER TABLE orders DISABLE TRIGGER trg_orders_business_request;
BEGIN;
SET CONSTRAINTS ALL DEFERRED;
INSERT INTO orders
 (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
  discount_amount,tax_amount,total_amount,balance_applied,payable_amount,
  expires_at,subscription_id,business_request_id)
VALUES
 ('93000000-0000-7000-8000-000000000403','93000000-0000-7000-8000-000000000001',
  'SETTLE-RENEWAL','93000000-0000-7000-8000-000000000012','renewal','pending_payment',
  'CNY',1000,0,0,1000,0,1000,now()+interval '30 minutes',
  '93000000-0000-7000-8000-000000000480','93000000-0000-7000-8000-000000000318');
INSERT INTO order_items
 (id,tenant_id,order_id,product_id,price_id,plan_id,plan_version_id,
  snapshot_product_name,snapshot_plan_name,snapshot_plan_version,
  snapshot_interval,snapshot_interval_count,quantity,unit_amount,line_amount,currency)
VALUES
 ('93000000-0000-7000-8000-000000000483','93000000-0000-7000-8000-000000000001',
  '93000000-0000-7000-8000-000000000403','93000000-0000-7000-8000-000000000023',
  '93000000-0000-7000-8000-000000000043','93000000-0000-7000-8000-000000000033',
  '93000000-0000-7000-8000-000000000053','Settle External','Settle External',1,
  'month',1,1,1000,1000,'CNY');
INSERT INTO order_reservations(id,tenant_id,order_id,user_id,expires_at) VALUES
 ('93000000-0000-7000-8000-000000000413','93000000-0000-7000-8000-000000000001',
  '93000000-0000-7000-8000-000000000403','93000000-0000-7000-8000-000000000012',
  now()+interval '30 minutes');
INSERT INTO order_reservation_events
 (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,business_request_id,actor_kind,actor_id)
VALUES
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000413',
  '93000000-0000-7000-8000-000000000403',NULL,'held','reserve',
  '93000000-0000-7000-8000-000000000318','system','93000000-0000-7000-8000-000000000012');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
ALTER TABLE orders ENABLE TRIGGER trg_orders_business_request;

-- Pre-existing terminal top-up orders.  Their released parent is the only
-- resource: a late callback must fail and roll back every attempted write.
BEGIN;
SET CONSTRAINTS ALL DEFERRED;
INSERT INTO orders
 (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
  discount_amount,tax_amount,total_amount,balance_applied,payable_amount,
  expires_at,idempotency_key_id)
VALUES
 ('93000000-0000-7000-8000-000000000401','93000000-0000-7000-8000-000000000001','SETTLE-CANCELLED','93000000-0000-7000-8000-000000000012','topup','pending_payment','CNY',500,0,0,500,0,500,now()+interval '30 minutes','93000000-0000-7000-8000-000000000305'),
 ('93000000-0000-7000-8000-000000000402','93000000-0000-7000-8000-000000000001','SETTLE-EXPIRED','93000000-0000-7000-8000-000000000012','topup','pending_payment','CNY',500,0,0,500,0,500,now()+interval '30 minutes','93000000-0000-7000-8000-000000000306');
INSERT INTO order_reservations(id,tenant_id,order_id,user_id,expires_at) VALUES
 ('93000000-0000-7000-8000-000000000411','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000401','93000000-0000-7000-8000-000000000012',now()+interval '30 minutes'),
 ('93000000-0000-7000-8000-000000000412','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000402','93000000-0000-7000-8000-000000000012',now()+interval '30 minutes');
INSERT INTO order_reservation_events
 (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,business_request_id,actor_kind,actor_id)
VALUES
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000411','93000000-0000-7000-8000-000000000401',NULL,'held','reserve','93000000-0000-7000-8000-000000000305','user','93000000-0000-7000-8000-000000000012'),
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000412','93000000-0000-7000-8000-000000000402',NULL,'held','reserve','93000000-0000-7000-8000-000000000306','user','93000000-0000-7000-8000-000000000012');
UPDATE order_reservations SET state='released',released_at=clock_timestamp(),release_reason='fixture_cancelled'
 WHERE id='93000000-0000-7000-8000-000000000411';
UPDATE order_reservations SET state='released',released_at=clock_timestamp(),release_reason='fixture_expired'
 WHERE id='93000000-0000-7000-8000-000000000412';
-- 终态预留事件。字段必须精确匹配 00040 的挂账守卫，不能用 fixture 自己的说法。
--
-- 守卫要求迟到支付所属的订单恰好有一条 held→released 事件，且：取消是
-- event_kind='cancel' + actor_kind='user' + actor_id=下单用户 + reason='user_cancelled'，
-- 过期是 event_kind='expire' + actor_kind='system' + actor_id IS NULL +
-- reason='reservation_expired'。这份 fixture 早于那道守卫，原来写的是
-- actor_kind='system' 和 reason='fixture_cancelled'/'fixture_expired'，
-- 于是两个 quarantine 用例都卡在「lacks its exact terminal reservation event」。
--
-- business_request_id 也必须取订单自己那一份——它由 trg_orders_business_request
-- 触发器填充，fixture 无从预知，只能回查；原来填的是 idempotency_key_id。
INSERT INTO order_reservation_events
 (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,business_request_id,actor_kind,actor_id,reason)
VALUES
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000411','93000000-0000-7000-8000-000000000401','held','released','cancel',
  (SELECT business_request_id FROM orders WHERE id='93000000-0000-7000-8000-000000000401'),
  'user','93000000-0000-7000-8000-000000000012','user_cancelled'),
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000412','93000000-0000-7000-8000-000000000402','held','released','expire',
  (SELECT business_request_id FROM orders WHERE id='93000000-0000-7000-8000-000000000402'),
  'system',NULL,'reservation_expired');
UPDATE orders SET status='cancelled',cancelled_at=clock_timestamp(),cancel_reason='fixture_cancelled'
 WHERE id='93000000-0000-7000-8000-000000000401';
UPDATE orders SET status='expired'
 WHERE id='93000000-0000-7000-8000-000000000402';
UPDATE idempotency_keys k
   SET resource_type='order',resource_id=o.id,status='succeeded',response_code=200,
       response_format='bytes',
       response_payload=convert_to('{"terminal":true}' || E'\n','UTF8'),
       response_content_type='application/json; charset=utf-8',
       completed_at=clock_timestamp(),locked_until=NULL
  FROM orders o
 WHERE k.id=o.idempotency_key_id
   AND o.id IN ('93000000-0000-7000-8000-000000000401','93000000-0000-7000-8000-000000000402');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;

-- Deliberately malformed graphs for fail-closed settlement probes. User
-- triggers are disabled only while planting these isolated negative fixtures.
ALTER TABLE orders DISABLE TRIGGER USER;
ALTER TABLE order_items DISABLE TRIGGER USER;
ALTER TABLE order_reservations DISABLE TRIGGER USER;
ALTER TABLE order_stock_reservations DISABLE TRIGGER USER;
ALTER TABLE order_reservation_events DISABLE TRIGGER USER;
ALTER TABLE plans DISABLE TRIGGER USER;
BEGIN;
INSERT INTO orders
 (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
  discount_amount,tax_amount,total_amount,balance_applied,payable_amount,
  expires_at,business_request_id)
VALUES
 ('93000000-0000-7000-8000-000000000404','93000000-0000-7000-8000-000000000001','SETTLE-CORRUPT-MISSING','93000000-0000-7000-8000-000000000012','new','pending_payment','CNY',1000,0,0,1000,0,1000,now()+interval '30 minutes','93000000-0000-7000-8000-000000000319'),
 ('93000000-0000-7000-8000-000000000405','93000000-0000-7000-8000-000000000001','SETTLE-CORRUPT-AMOUNT','93000000-0000-7000-8000-000000000012','new','pending_payment','CNY',1000,0,0,1000,0,1000,now()+interval '30 minutes','93000000-0000-7000-8000-000000000320');
INSERT INTO order_items
 (id,tenant_id,order_id,product_id,price_id,plan_id,plan_version_id,
  snapshot_product_name,snapshot_plan_name,snapshot_plan_version,
  snapshot_interval,snapshot_interval_count,quantity,unit_amount,line_amount,currency)
VALUES
 ('93000000-0000-7000-8000-000000000484','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000404','93000000-0000-7000-8000-000000000029','93000000-0000-7000-8000-000000000049','93000000-0000-7000-8000-000000000039','93000000-0000-7000-8000-000000000059','Settle Corrupt Fixture','Settle Corrupt Fixture',1,'month',1,1,1000,1000,'CNY'),
 ('93000000-0000-7000-8000-000000000485','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000405','93000000-0000-7000-8000-000000000029','93000000-0000-7000-8000-000000000049','93000000-0000-7000-8000-000000000039','93000000-0000-7000-8000-000000000059','Settle Corrupt Fixture','Settle Corrupt Fixture',1,'month',1,1,900,900,'CNY');
INSERT INTO order_reservations(id,tenant_id,order_id,user_id,expires_at) VALUES
 ('93000000-0000-7000-8000-000000000414','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000404','93000000-0000-7000-8000-000000000012',now()+interval '30 minutes'),
 ('93000000-0000-7000-8000-000000000415','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000405','93000000-0000-7000-8000-000000000012',now()+interval '30 minutes');
INSERT INTO order_stock_reservations(id,tenant_id,reservation_id,order_id,plan_id,quantity)
VALUES('93000000-0000-7000-8000-000000000486','93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000415','93000000-0000-7000-8000-000000000405','93000000-0000-7000-8000-000000000039',1);
UPDATE plans SET stock_reserved=stock_reserved+1 WHERE id='93000000-0000-7000-8000-000000000039';
INSERT INTO order_reservation_events
 (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,business_request_id,actor_kind,actor_id)
VALUES
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000414','93000000-0000-7000-8000-000000000404',NULL,'held','reserve','93000000-0000-7000-8000-000000000319','system','93000000-0000-7000-8000-000000000012'),
 ('93000000-0000-7000-8000-000000000001','93000000-0000-7000-8000-000000000415','93000000-0000-7000-8000-000000000405',NULL,'held','reserve','93000000-0000-7000-8000-000000000320','system','93000000-0000-7000-8000-000000000012');
COMMIT;
ALTER TABLE orders ENABLE TRIGGER USER;
ALTER TABLE order_items ENABLE TRIGGER USER;
ALTER TABLE order_reservations ENABLE TRIGGER USER;
ALTER TABLE order_stock_reservations ENABLE TRIGGER USER;
ALTER TABLE order_reservation_events ENABLE TRIGGER USER;
ALTER TABLE plans ENABLE TRIGGER USER;

-- Valid tenant-A evidence used from a tenant-B session to prove FORCE RLS hides
-- an existing foreign-tenant commission row rather than an empty table.
-- 这一行只为证明「外租户的佣金记录存在、但被 FORCE RLS 挡住」而存在，
-- 不承载任何业务语义。
--
-- 00040 的 app.assert_commission_entry() 要求佣金分录背后有一整条账务证据
-- 链：referrals 关系、对应订单、一条借记 platform_revenue 且金额与币种逐项
-- 匹配的分录、账户的 owner_ref 与 normal_balance 也要对得上。为一行可见性
-- 样本构造这些，等于让 fixture 跟着佣金账务模型一起漂移——那个模型每变一次，
-- 这份与它无关的 checkout fixture 就得跟着改一次。
--
-- 所以这里临时切到 replica 会话角色把触发器停掉，插完立刻切回。代价是这行
-- 数据在业务上是不自洽的；它也只被用来断言「看不见」，不参与任何业务路径。
SET session_replication_role = replica;
INSERT INTO commission_entries
 (id,tenant_id,referrer_user_id,referee_user_id,order_id,currency,
  base_amount,rate_bp,commission_amount,frozen_until,review_required)
VALUES
 ('93000000-0000-7000-8000-000000000491','93000000-0000-7000-8000-000000000001',
  '93000000-0000-7000-8000-000000000013','93000000-0000-7000-8000-000000000014',
  '93000000-0000-7000-8000-000000000401','CNY',500,1000,50,now()+interval '1 day',false);
SET session_replication_role = origin;

CREATE SEQUENCE app.aegis_test_fault_after_ledger_seq START 1;
CREATE SEQUENCE app.aegis_test_fault_after_capture_seq START 1;
CREATE SEQUENCE app.aegis_test_fault_after_fulfil_seq START 1;
CREATE SEQUENCE app.aegis_test_fault_after_audit_seq START 1;
REVOKE ALL ON SEQUENCE app.aegis_test_fault_after_ledger_seq,
 app.aegis_test_fault_after_capture_seq,app.aegis_test_fault_after_fulfil_seq,
 app.aegis_test_fault_after_audit_seq FROM PUBLIC,aegis_app;

CREATE FUNCTION app.aegis_test_fault_after_ledger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,app AS $$
DECLARE v_plan uuid; v_entries int;
BEGIN
  SELECT oi.plan_id INTO v_plan
    FROM ledger_transactions t JOIN order_items oi ON oi.order_id=t.source_id
   WHERE t.id=NEW.transaction_id AND t.kind='order_paid' AND t.source_type='order';
  IF v_plan='93000000-0000-7000-8000-000000000035'::uuid THEN
    SELECT count(*) INTO v_entries FROM ledger_entries WHERE transaction_id=NEW.transaction_id;
    IF v_entries=2 AND nextval('app.aegis_test_fault_after_ledger_seq')=1 THEN
      RAISE EXCEPTION 'settlement fault after ledger' USING ERRCODE='P0001';
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER aegis_test_fault_after_ledger
  AFTER INSERT ON ledger_entries FOR EACH ROW
  EXECUTE FUNCTION app.aegis_test_fault_after_ledger();

CREATE FUNCTION app.aegis_test_fault_after_capture() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,app AS $$
BEGIN
  IF NEW.to_state='captured'
     AND NEW.business_request_id='93000000-0000-7000-8000-000000000313'::uuid
     AND nextval('app.aegis_test_fault_after_capture_seq')=1 THEN
    RAISE EXCEPTION 'settlement fault after capture' USING ERRCODE='P0001';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER aegis_test_fault_after_capture
  AFTER INSERT ON order_reservation_events FOR EACH ROW
  EXECUTE FUNCTION app.aegis_test_fault_after_capture();

CREATE FUNCTION app.aegis_test_fault_after_fulfil() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,app AS $$
BEGIN
  IF NEW.provider_event_id='settle-fault-fulfil-event'
     AND NEW.processing_status='processed'
     AND nextval('app.aegis_test_fault_after_fulfil_seq')=1 THEN
    RAISE EXCEPTION 'settlement fault after fulfil' USING ERRCODE='P0001';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER aegis_test_fault_after_fulfil
  BEFORE UPDATE ON payment_events FOR EACH ROW
  EXECUTE FUNCTION app.aegis_test_fault_after_fulfil();

CREATE FUNCTION app.aegis_test_fault_after_audit() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,app AS $$
DECLARE v_business_request uuid;
BEGIN
  IF NEW.action='payment.succeeded' AND NEW.resource_type='order' THEN
    SELECT business_request_id INTO v_business_request FROM orders WHERE id=NEW.resource_id;
    IF v_business_request='93000000-0000-7000-8000-000000000315'::uuid
       AND nextval('app.aegis_test_fault_after_audit_seq')=1 THEN
      RAISE EXCEPTION 'settlement fault after audit' USING ERRCODE='P0001';
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER aegis_test_fault_after_audit
  AFTER INSERT ON audit_events FOR EACH ROW
  EXECUTE FUNCTION app.aegis_test_fault_after_audit();

REVOKE ALL ON FUNCTION app.aegis_test_fault_after_ledger(),
 app.aegis_test_fault_after_capture(),app.aegis_test_fault_after_fulfil(),
 app.aegis_test_fault_after_audit() FROM PUBLIC,aegis_app;
