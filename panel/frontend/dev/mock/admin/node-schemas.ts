// prettier-ignore
export const NODE_PROTOCOL_SCHEMAS = {
  "schemas": [
    {
      "node_type": "shadowsocks",
      "version": 1,
      "status": "stable",
      "required": [
        "cipher"
      ],
      "allowed_properties": [
        "cipher"
      ],
      "methods": [
        "aes-128-gcm",
        "aes-256-gcm",
        "chacha20-ietf-poly1305"
      ]
    },
    {
      "node_type": "hysteria2",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "cert_path",
        "key_path",
        "obfs.type",
        "obfs.password",
        "bandwidth.up",
        "bandwidth.down",
        "udp_timeout",
        "tls_settings.server_name",
        "tls_settings.allow_insecure"
      ],
      "enums": {
        "network": [
          "udp"
        ],
        "obfs.type": [
          "salamander"
        ]
      },
      "property_types": {
        "bandwidth.down": "number",
        "bandwidth.up": "number",
        "tls_settings.allow_insecure": "boolean"
      },
      "sensitive_properties": [
        "obfs.password"
      ],
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      }
    },
    {
      "node_type": "juicity",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "cert_path",
        "key_path",
        "congestion_control"
      ],
      "enums": {
        "congestion_control": [
          "cubic",
          "new_reno",
          "bbr"
        ],
        "network": [
          "udp"
        ]
      },
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      }
    },
    {
      "node_type": "socks",
      "version": 1,
      "status": "stable",
      "required": null,
      "allowed_properties": [
        "network",
        "tls",
        "cert_path",
        "key_path",
        "security"
      ],
      "enums": {
        "network": [
          "tcp",
          "udp"
        ],
        "security": [
          "none"
        ]
      },
      "property_types": {
        "tls": "boolean"
      },
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      }
    },
    {
      "node_type": "http",
      "version": 1,
      "status": "stable",
      "required": null,
      "allowed_properties": [
        "network",
        "tls",
        "cert_path",
        "key_path",
        "security"
      ],
      "enums": {
        "network": [
          "tcp"
        ],
        "security": [
          "none"
        ]
      },
      "property_types": {
        "tls": "boolean"
      },
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      }
    },
    {
      "node_type": "naive",
      "version": 1,
      "status": "stable",
      "required": [
        "tls",
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "tls",
        "cert_path",
        "key_path",
        "security",
        "tls_settings.server_name",
        "tls_settings.allow_insecure",
        "fallback"
      ],
      "enums": {
        "network": [
          "tcp"
        ],
        "security": [
          "none"
        ]
      },
      "property_types": {
        "tls": "boolean",
        "tls_settings.allow_insecure": "boolean"
      },
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "fallback": "选填，host:port。回落目标是一个明文 HTTP 站点，认证失败的探测会被转过去，让节点看起来像个普通网站；可以填本机（如 127.0.0.1:80 的本机 nginx），不能填内网地址。留空时回一个中性的 404 页面。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      }
    },
    {
      "node_type": "mieru",
      "version": 1,
      "status": "stable",
      "required": null,
      "allowed_properties": [
        "transport"
      ],
      "enums": {
        "transport": [
          "TCP",
          "UDP"
        ]
      }
    },
    {
      "node_type": "shadowtls",
      "version": 1,
      "status": "stable",
      "required": [
        "password"
      ],
      "allowed_properties": [
        "network",
        "version",
        "password",
        "server",
        "handshake_server",
        "server_port",
        "method",
        "strict",
        "wildcard_sni"
      ],
      "enums": {
        "method": [
          "aes-128-gcm",
          "aes-256-gcm",
          "chacha20-ietf-poly1305"
        ],
        "network": [
          "tcp"
        ],
        "wildcard_sni": [
          "off",
          "authed",
          "all"
        ]
      },
      "property_types": {
        "server_port": "number",
        "strict": "boolean",
        "version": "number"
      },
      "sensitive_properties": [
        "password"
      ]
    },
    {
      "node_type": "tuic",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "cert_path",
        "key_path",
        "congestion_control",
        "auth_timeout",
        "heartbeat",
        "udp_timeout",
        "zero_rtt",
        "tls_settings.server_name",
        "tls_settings.allow_insecure"
      ],
      "enums": {
        "congestion_control": [
          "cubic",
          "new_reno",
          "bbr"
        ],
        "network": [
          "udp"
        ]
      },
      "property_types": {
        "tls_settings.allow_insecure": "boolean",
        "zero_rtt": "boolean"
      },
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      }
    },
    {
      "node_type": "anytls",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "tls",
        "cert_path",
        "key_path",
        "padding_scheme",
        "tls_settings.server_name",
        "tls_settings.allow_insecure",
        "utls",
        "fallback"
      ],
      "enums": {
        "network": [
          "tcp"
        ],
        "utls": [
          "chrome",
          "firefox",
          "safari",
          "ios",
          "android",
          "edge",
          "360",
          "qq",
          "random"
        ]
      },
      "property_types": {
        "padding_scheme": "json",
        "tls": "boolean",
        "tls_settings.allow_insecure": "boolean"
      },
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "fallback": "选填，host:port。回落目标是一个明文 HTTP 站点，认证失败的探测会被转过去，让节点看起来像个普通网站；可以填本机（如 127.0.0.1:80 的本机 nginx），不能填内网地址。留空时回一个中性的 404 页面。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "utls": "客户端模仿的浏览器 TLS 指纹，订阅三种格式都会下发；留空按 chrome。"
      }
    },
    {
      "node_type": "trojan",
      "version": 1,
      "status": "stable",
      "required": [
        "tls"
      ],
      "allowed_properties": [
        "network",
        "tls",
        "utls",
        "network_settings.path",
        "network_settings.headers.Host",
        "network_settings.serviceName",
        "network_settings.mode",
        "ws_path",
        "grpc_path",
        "cert_path",
        "key_path",
        "reality_settings.dest",
        "reality_settings.server_name",
        "reality_settings.private_key",
        "reality_settings.public_key",
        "reality_settings.short_id",
        "fallback",
        "tls_settings.server_name",
        "tls_settings.allow_insecure",
        "mtu",
        "tti",
        "uplink_capacity",
        "downlink_capacity",
        "congestion",
        "read_buffer_size",
        "write_buffer_size",
        "mask",
        "mask_password"
      ],
      "enums": {
        "network": [
          "tcp",
          "ws",
          "httpupgrade",
          "grpc",
          "mkcp"
        ],
        "tls": [
          "1",
          "2"
        ],
        "utls": [
          "chrome",
          "firefox",
          "safari",
          "ios",
          "android",
          "edge",
          "360",
          "qq",
          "random"
        ]
      },
      "property_types": {
        "congestion": "boolean",
        "downlink_capacity": "number",
        "mtu": "number",
        "read_buffer_size": "number",
        "reality_settings.server_name": "list",
        "reality_settings.short_id": "list",
        "tls": "number",
        "tls_settings.allow_insecure": "boolean",
        "tti": "number",
        "uplink_capacity": "number",
        "write_buffer_size": "number"
      },
      "sensitive_properties": [
        "private_key",
        "mask_password"
      ],
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "fallback": "选填，host:port。回落目标是一个明文 HTTP 站点，认证失败的探测会被转过去，让节点看起来像个普通网站；可以填本机（如 127.0.0.1:80 的本机 nginx），不能填内网地址。留空时回一个中性的 404 页面。只在 tcp 传输上生效。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "reality_settings.dest": "借用握手的真实公网站点，域名:端口，例如 www.example.com:443；不能填 IP、localhost 或内网域名。",
        "reality_settings.server_name": "可填多个，用逗号分隔；每个用户的订阅按固定规则分到其中一个，分散特征。",
        "reality_settings.short_id": "必填，可填多个（逗号分隔），每个不超过 16 位十六进制；每个用户的订阅分到其中一个。",
        "utls": "客户端模仿的浏览器 TLS 指纹，订阅三种格式都会下发；留空按 chrome。"
      }
    },
    {
      "node_type": "vless",
      "version": 1,
      "status": "stable",
      "required": null,
      "allowed_properties": [
        "network",
        "tls",
        "utls",
        "network_settings.path",
        "network_settings.headers.Host",
        "network_settings.serviceName",
        "network_settings.mode",
        "ws_path",
        "grpc_path",
        "headers",
        "sc_max_each_post_bytes",
        "sc_min_posts_interval_ms",
        "sc_max_buffered_posts",
        "sc_stream_up_server_secs",
        "session_placement",
        "session_key",
        "seq_placement",
        "seq_key",
        "uplink_http_method",
        "uplink_data_placement",
        "uplink_data_key",
        "uplink_chunk_size",
        "server_max_header_bytes",
        "cert_path",
        "key_path",
        "mtu",
        "tti",
        "uplink_capacity",
        "downlink_capacity",
        "congestion",
        "read_buffer_size",
        "write_buffer_size",
        "mask",
        "mask_password",
        "reality_settings.dest",
        "reality_settings.server_name",
        "reality_settings.private_key",
        "reality_settings.public_key",
        "reality_settings.short_id",
        "flow",
        "fallback"
      ],
      "enums": {
        "flow": [
          "xtls-rprx-vision",
          "xtls-rprx-vision-udp443"
        ],
        "mode": [
          "auto",
          "packet-up",
          "stream-up",
          "stream-one",
          "stream-down"
        ],
        "network": [
          "tcp",
          "ws",
          "httpupgrade",
          "grpc",
          "xhttp",
          "xhttp-h3",
          "mkcp"
        ],
        "seq_placement": [
          "path",
          "query",
          "header",
          "cookie"
        ],
        "session_placement": [
          "path",
          "query",
          "header",
          "cookie"
        ],
        "tls": [
          "0",
          "2"
        ],
        "uplink_data_placement": [
          "body",
          "query",
          "header",
          "cookie"
        ],
        "uplink_http_method": [
          "GET",
          "POST"
        ],
        "utls": [
          "chrome",
          "firefox",
          "safari",
          "ios",
          "android",
          "edge",
          "360",
          "qq",
          "random"
        ]
      },
      "property_types": {
        "congestion": "boolean",
        "downlink_capacity": "number",
        "headers": "json",
        "mtu": "number",
        "read_buffer_size": "number",
        "reality_settings.server_name": "list",
        "reality_settings.short_id": "list",
        "sc_max_buffered_posts": "number",
        "sc_max_each_post_bytes": "json",
        "sc_min_posts_interval_ms": "json",
        "sc_stream_up_server_secs": "json",
        "server_max_header_bytes": "number",
        "tls": "number",
        "tti": "number",
        "uplink_capacity": "number",
        "uplink_chunk_size": "json",
        "write_buffer_size": "number"
      },
      "sensitive_properties": [
        "private_key",
        "mask_password"
      ],
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "fallback": "选填，host:port。回落目标是一个明文 HTTP 站点，认证失败的探测会被转过去，让节点看起来像个普通网站；可以填本机（如 127.0.0.1:80 的本机 nginx），不能填内网地址。留空时回一个中性的 404 页面。只在 tcp 传输上生效。",
        "flow": "REALITY + tcp 时用 xtls-rprx-vision（默认）；其它传输必须留空。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "reality_settings.dest": "借用握手的真实公网站点，域名:端口，例如 www.example.com:443；不能填 IP、localhost 或内网域名。",
        "reality_settings.server_name": "可填多个，用逗号分隔；每个用户的订阅按固定规则分到其中一个，分散特征。",
        "reality_settings.short_id": "必填，可填多个（逗号分隔），每个不超过 16 位十六进制；每个用户的订阅分到其中一个。",
        "utls": "客户端模仿的浏览器 TLS 指纹，订阅三种格式都会下发；留空按 chrome。"
      }
    },
    {
      "node_type": "vmess",
      "version": 1,
      "status": "stable",
      "required": null,
      "allowed_properties": [
        "network",
        "tls",
        "utls",
        "network_settings.path",
        "network_settings.headers.Host",
        "network_settings.serviceName",
        "network_settings.mode",
        "ws_path",
        "grpc_path",
        "headers",
        "sc_max_each_post_bytes",
        "sc_min_posts_interval_ms",
        "sc_max_buffered_posts",
        "sc_stream_up_server_secs",
        "session_placement",
        "session_key",
        "seq_placement",
        "seq_key",
        "uplink_http_method",
        "uplink_data_placement",
        "uplink_data_key",
        "uplink_chunk_size",
        "server_max_header_bytes",
        "cert_path",
        "key_path",
        "mtu",
        "tti",
        "uplink_capacity",
        "downlink_capacity",
        "congestion",
        "read_buffer_size",
        "write_buffer_size",
        "mask",
        "mask_password",
        "security"
      ],
      "enums": {
        "mode": [
          "auto",
          "packet-up",
          "stream-up",
          "stream-one",
          "stream-down"
        ],
        "network": [
          "tcp",
          "ws",
          "httpupgrade",
          "grpc",
          "xhttp",
          "xhttp-h3",
          "mkcp"
        ],
        "security": [
          "none",
          "zero",
          "aes-128-gcm",
          "chacha20-poly1305",
          "auto"
        ],
        "seq_placement": [
          "path",
          "query",
          "header",
          "cookie"
        ],
        "session_placement": [
          "path",
          "query",
          "header",
          "cookie"
        ],
        "tls": [
          "0"
        ],
        "uplink_data_placement": [
          "body",
          "query",
          "header",
          "cookie"
        ],
        "uplink_http_method": [
          "GET",
          "POST"
        ],
        "utls": [
          "chrome",
          "firefox",
          "safari",
          "ios",
          "android",
          "edge",
          "360",
          "qq",
          "random"
        ]
      },
      "property_types": {
        "congestion": "boolean",
        "downlink_capacity": "number",
        "headers": "json",
        "mtu": "number",
        "read_buffer_size": "number",
        "sc_max_buffered_posts": "number",
        "sc_max_each_post_bytes": "json",
        "sc_min_posts_interval_ms": "json",
        "sc_stream_up_server_secs": "json",
        "server_max_header_bytes": "number",
        "tls": "number",
        "tti": "number",
        "uplink_capacity": "number",
        "uplink_chunk_size": "json",
        "write_buffer_size": "number"
      },
      "hints": {
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "network": "VMess 不能开 TLS，只能用 ws / httpupgrade / grpc / xhttp 并套 CDN 或 TLS 反代；裸 tcp 不允许。"
      }
    },
    {
      "node_type": "v2ray",
      "version": 0,
      "status": "legacy-read-compatible",
      "required": null,
      "allowed_properties": null
    },
    {
      "node_type": "hysteria",
      "version": 0,
      "status": "legacy-read-compatible",
      "required": null,
      "allowed_properties": null
    }
  ]
} as const
