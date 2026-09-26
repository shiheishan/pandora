/**
 * [INPUT]: 无
 * [OUTPUT]: 对外提供 NODE_PROTOCOL_SCHEMAS（GET v1/node-protocol-schemas 的完整响应）
 * [POS]: dev/mock/admin 的协议 schema 夹具，归后台前端二：由 Go 的 nodefabric.ProtocolSchemas() 在 9a6ce9f 上原样导出（json.Marshal），含 13 个 stable 协议与 2 个 legacy-read-compatible（v2ray / hysteria）；Go 的 nil 切片序列化成 null，这里照留，页面的 zod 必须接得住。后端改 schema 时按同样方法重新导出
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

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
        "udp_timeout"
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
        "bandwidth.up": "number"
      },
      "sensitive_properties": [
        "obfs.password"
      ]
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
        "zero_rtt"
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
        "zero_rtt": "boolean"
      }
    },
    {
      "node_type": "anytls",
      "version": 1,
      "status": "stable",
      "required": null,
      "allowed_properties": [
        "network",
        "tls",
        "cert_path",
        "key_path",
        "padding_scheme"
      ],
      "enums": {
        "network": [
          "tcp"
        ]
      },
      "property_types": {
        "padding_scheme": "json",
        "tls": "boolean"
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
        "flow",
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
        ]
      },
      "property_types": {
        "congestion": "boolean",
        "downlink_capacity": "number",
        "mtu": "number",
        "read_buffer_size": "number",
        "tls": "number",
        "tti": "number",
        "uplink_capacity": "number",
        "write_buffer_size": "number"
      },
      "sensitive_properties": [
        "private_key",
        "mask_password"
      ]
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
        "flow"
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
      "sensitive_properties": [
        "private_key",
        "mask_password"
      ]
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
