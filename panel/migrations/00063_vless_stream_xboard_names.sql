-- +goose Up
-- vless / vmess / trojan 的协议配置改用 xboard 的嵌套形状。
--
-- 这是改名的最后一批，也是唯一一批动到**正在跑**的节点，所以口径比前两批
-- 更严：迁移之后每个节点算一次 toKernelConfig，结果必须和迁移前逐字段
-- 相同，一个字节都不能差。
--
-- 三处改动：
--
--   security 枚举  →  tls 三态整数     none → 0、reality → 2
--   REALITY 各项   →  reality_settings 对象
--   fingerprint    →  utls
--
-- 关于 security → tls 的方向，有一点特别容易搞反：xboard 的 tls=2 表示
-- REALITY，但我们的校验器**明确禁止** tls=true（"证书生命周期完成前仅允许
-- tls=false，请改用 security=reality"）。所以 2 在翻译回来时对应的是
-- security=reality，不是 tls=true。映射反了会被自己的校验器拒掉。
--
-- 迁移后不写 tls:false 之外的东西，也不给原本没有 tls 键的节点凭空补一个：
-- 现网 4 个在役 vless 存的就是「只有 security、没有 tls」，补了就改变了
-- 下发内容。
--
-- 已退役节点里有 5 个存着 tls:1（普通 TLS，早于当前校验规则的历史数据）。
-- 它们不动：永远不会被下发，而翻译层对 1 也是原样透传。

-- security → tls 三态。只处理这两个已知值，其它值留着不动，
-- 免得把没预料到的数据静默改坏。
-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = (protocol_config - 'security')
                         || jsonb_build_object('tls',
                              case protocol_config->>'security'
                                when 'reality' then 2
                                when 'none'    then 0
                              end)
 WHERE node_type IN ('vless', 'vmess', 'trojan')
   AND protocol_config->>'security' IN ('reality', 'none')
   AND NOT protocol_config ? 'tls';
-- +goose StatementEnd

-- REALITY 各项收进 reality_settings。
-- server_names / short_ids 在我们这边是数组，xboard 是单个字符串，
-- 取第一个元素——现网每个节点都恰好只有一个。多于一个的不动，宁可留着
-- 让人来看，也不能悄悄丢掉一个。
-- +goose StatementBegin
UPDATE nodes
   SET protocol_config =
         (protocol_config - 'dest' - 'server_names' - 'short_ids'
                          - 'public_key' - 'private_key')
         || jsonb_build_object('reality_settings',
              coalesce(protocol_config->'reality_settings', '{}'::jsonb)
              || case when protocol_config ? 'dest'
                      then jsonb_build_object('dest', protocol_config->'dest')
                      else '{}'::jsonb end
              || case when protocol_config ? 'public_key'
                      then jsonb_build_object('public_key', protocol_config->'public_key')
                      else '{}'::jsonb end
              || case when protocol_config ? 'private_key'
                      then jsonb_build_object('private_key', protocol_config->'private_key')
                      else '{}'::jsonb end
              || case when jsonb_array_length(coalesce(protocol_config->'server_names','[]'::jsonb)) = 1
                      then jsonb_build_object('server_name', protocol_config->'server_names'->0)
                      else '{}'::jsonb end
              || case when jsonb_array_length(coalesce(protocol_config->'short_ids','[]'::jsonb)) = 1
                      then jsonb_build_object('short_id', protocol_config->'short_ids'->0)
                      else '{}'::jsonb end)
 WHERE node_type IN ('vless', 'vmess', 'trojan')
   AND (protocol_config ? 'dest' OR protocol_config ? 'public_key'
        OR protocol_config ? 'private_key')
   AND jsonb_array_length(coalesce(protocol_config->'server_names', '[]'::jsonb)) <= 1
   AND jsonb_array_length(coalesce(protocol_config->'short_ids', '[]'::jsonb)) <= 1;
-- +goose StatementEnd

-- fingerprint → utls
-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = (protocol_config - 'fingerprint')
                         || jsonb_build_object('utls', protocol_config->'fingerprint')
 WHERE node_type IN ('vless', 'vmess', 'trojan')
   AND protocol_config ? 'fingerprint';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = (protocol_config - 'utls')
                         || jsonb_build_object('fingerprint', protocol_config->'utls')
 WHERE node_type IN ('vless', 'vmess', 'trojan')
   AND protocol_config ? 'utls';
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE nodes
   SET protocol_config =
         (protocol_config - 'reality_settings')
         || case when protocol_config->'reality_settings' ? 'dest'
                 then jsonb_build_object('dest', protocol_config->'reality_settings'->'dest')
                 else '{}'::jsonb end
         || case when protocol_config->'reality_settings' ? 'public_key'
                 then jsonb_build_object('public_key', protocol_config->'reality_settings'->'public_key')
                 else '{}'::jsonb end
         || case when protocol_config->'reality_settings' ? 'private_key'
                 then jsonb_build_object('private_key', protocol_config->'reality_settings'->'private_key')
                 else '{}'::jsonb end
         || case when protocol_config->'reality_settings' ? 'server_name'
                 then jsonb_build_object('server_names',
                        jsonb_build_array(protocol_config->'reality_settings'->'server_name'))
                 else '{}'::jsonb end
         || case when protocol_config->'reality_settings' ? 'short_id'
                 then jsonb_build_object('short_ids',
                        jsonb_build_array(protocol_config->'reality_settings'->'short_id'))
                 else '{}'::jsonb end
 WHERE node_type IN ('vless', 'vmess', 'trojan')
   AND protocol_config ? 'reality_settings';
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = (protocol_config - 'tls')
                         || jsonb_build_object('security',
                              case protocol_config->>'tls'
                                when '2' then 'reality'
                                when '0' then 'none'
                              end)
 WHERE node_type IN ('vless', 'vmess', 'trojan')
   AND protocol_config->>'tls' IN ('0', '2');
-- +goose StatementEnd
