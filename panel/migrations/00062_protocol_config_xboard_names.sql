-- +goose Up
-- 存量节点的协议配置换成 xboard 的字段名。
--
-- 后台的协议参数要和 xboard 一模一样，运维照着别处的教程就能填。字段名
-- 改了，但下发给节点的内容不能变——protocol_config 原来是原样平铺发给
-- 内核的，现在中间隔了一层 toKernelConfig 把 xboard 名翻译回内核名
-- （见 internal/domain/nodefabric/xboard_field_names.go）。
--
-- 所以这条迁移和那层翻译是严格互逆的一对：
--
--   迁移：  method → cipher          （库里存的换成 xboard 名）
--   翻译：  cipher → method          （下发时换回内核名）
--
-- 两边对不上的后果不是报错，是节点拿到一份缺字段的配置然后静默连不上。
-- 验证方式是拿真实数据跑一遍：迁移后对每个节点算一次 toKernelConfig，
-- 结果必须和迁移前的 protocol_config 逐字段相同。
--
-- 这一批只动三个协议，因为只有它们的字段名在这一轮改了：
--
--   shadowsocks  method → cipher
--   mieru        transport 转大写（xboard 用 TCP / UDP）
--   hysteria2    up_mbps / down_mbps → bandwidth.up / bandwidth.down
--
-- vless / vmess / trojan 的 network_settings 嵌套改造不在这一批，它们的
-- 字段多、REALITY 那套还要单独处理，分开走以便出问题时能单独回滚。

-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = (protocol_config - 'method')
                         || jsonb_build_object('cipher', protocol_config->'method')
 WHERE node_type = 'shadowsocks'
   AND protocol_config ? 'method';
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = jsonb_set(protocol_config, '{transport}',
                                   to_jsonb(upper(protocol_config->>'transport')))
 WHERE node_type = 'mieru'
   AND protocol_config ? 'transport'
   AND protocol_config->>'transport' IS NOT NULL
   AND protocol_config->>'transport' <> upper(protocol_config->>'transport');
-- +goose StatementEnd

-- 带宽两项合进 bandwidth 对象。分两步写会在中间留下「有 bandwidth.up
-- 但还有 up_mbps」的状态，一次做完更简单也更好回滚。
-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = (protocol_config - 'up_mbps' - 'down_mbps')
     || jsonb_build_object('bandwidth',
          coalesce(protocol_config->'bandwidth', '{}'::jsonb)
          || case when protocol_config ? 'up_mbps'
                  then jsonb_build_object('up', protocol_config->'up_mbps')
                  else '{}'::jsonb end
          || case when protocol_config ? 'down_mbps'
                  then jsonb_build_object('down', protocol_config->'down_mbps')
                  else '{}'::jsonb end)
 WHERE node_type = 'hysteria2'
   AND (protocol_config ? 'up_mbps' OR protocol_config ? 'down_mbps');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = (protocol_config - 'cipher')
                         || jsonb_build_object('method', protocol_config->'cipher')
 WHERE node_type = 'shadowsocks'
   AND protocol_config ? 'cipher';
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = jsonb_set(protocol_config, '{transport}',
                                   to_jsonb(lower(protocol_config->>'transport')))
 WHERE node_type = 'mieru'
   AND protocol_config ? 'transport';
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE nodes
   SET protocol_config = (protocol_config - 'bandwidth')
     || case when protocol_config->'bandwidth' ? 'up'
             then jsonb_build_object('up_mbps', protocol_config->'bandwidth'->'up')
             else '{}'::jsonb end
     || case when protocol_config->'bandwidth' ? 'down'
             then jsonb_build_object('down_mbps', protocol_config->'bandwidth'->'down')
             else '{}'::jsonb end
 WHERE node_type = 'hysteria2'
   AND protocol_config ? 'bandwidth';
-- +goose StatementEnd
