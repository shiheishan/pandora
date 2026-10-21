// prettier-ignore
export const NODE_PROTOCOL_SCHEMAS = {
  "schemas": [
    {
      "node_type": "anytls",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_mode",
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "tls",
        "cert_mode",
        "cert_path",
        "key_path",
        "padding_scheme",
        "tls_settings.server_name",
        "tls_settings.allow_insecure",
        "utls",
        "fallback"
      ],
      "enums": {
        "cert_mode": [
          "acme",
          "file"
        ],
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
        "cert_mode": "自动申请：节点端用本机 80 端口向 Let's Encrypt 申请并自动续期，80 端口要能从公网访问。证书名字用「客户端 SNI」，没填就用节点的连接地址，可以是公网 IP（证书约 6 天一换）或域名（约 90 天）。选自动申请即视为同意 Let's Encrypt 订户协议（https://letsencrypt.org/repository/）。手填：自己把证书放到节点机上，填路径。",
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "fallback": "选填，host:port。回落目标是一个明文 HTTP 站点，认证失败的探测会被转过去，让节点看起来像个普通网站；可以填本机（如 127.0.0.1:80 的本机 nginx），不能填内网地址。留空时回一个中性的 404 页面。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "tls_settings.allow_insecure": "只进订阅；手填自签证书时才打开，自动申请的证书不需要。",
        "tls_settings.server_name": "只进订阅，告诉客户端握手用的域名。",
        "utls": "客户端模仿的浏览器 TLS 指纹，订阅三种格式都会下发；留空按 chrome。"
      },
      "note": "证书必填（默认自动申请）：没证书时节点会以明文起，而客户端都强制 TLS，存得进去却谁都连不上。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "tcp"
        },
        {
          "path": "tls",
          "label": "开启 TLS"
        },
        {
          "path": "cert_mode",
          "label": "证书来源",
          "default": "acme",
          "option_labels": {
            "acme": "自动申请（Let's Encrypt）",
            "file": "手填证书路径"
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "padding_scheme",
          "label": "填充方案",
          "advanced": true,
          "placeholder": "[\"stop=8\",\"0=30-30\"]"
        },
        {
          "path": "utls",
          "label": "uTLS 指纹",
          "option_labels": {
            "": "留空（chrome）"
          }
        },
        {
          "path": "tls_settings.server_name",
          "label": "客户端 SNI",
          "placeholder": "example.com"
        },
        {
          "path": "tls_settings.allow_insecure",
          "label": "允许不安全证书",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          }
        },
        {
          "path": "fallback",
          "label": "回落地址",
          "placeholder": "127.0.0.1:80"
        }
      ]
    },
    {
      "node_type": "http",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_mode"
      ],
      "allowed_properties": [
        "network",
        "tls",
        "cert_mode",
        "cert_path",
        "key_path",
        "security"
      ],
      "enums": {
        "cert_mode": [
          "acme",
          "file"
        ],
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
        "cert_mode": "自动申请：节点端用本机 80 端口向 Let's Encrypt 申请并自动续期，80 端口要能从公网访问。证书名字用「客户端 SNI」，没填就用节点的连接地址，可以是公网 IP（证书约 6 天一换）或域名（约 90 天）。选自动申请即视为同意 Let's Encrypt 订户协议（https://letsencrypt.org/repository/）。手填：自己把证书放到节点机上，填路径。",
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      },
      "note": "开 TLS 时需要证书：默认自动申请，也可以手填路径。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "tcp"
        },
        {
          "path": "tls",
          "label": "开启 TLS",
          "default": false
        },
        {
          "path": "cert_mode",
          "label": "证书来源",
          "show_if": {
            "tls": [
              "true"
            ]
          },
          "default": "acme",
          "option_labels": {
            "acme": "自动申请（Let's Encrypt）",
            "file": "手填证书路径"
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "cert_mode": [
              "file"
            ],
            "tls": [
              "true"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "cert_mode": [
              "file"
            ],
            "tls": [
              "true"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "security",
          "label": "security",
          "advanced": true,
          "default": "none"
        }
      ]
    },
    {
      "node_type": "hysteria2",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_mode",
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "cert_mode",
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
        "cert_mode": [
          "acme",
          "file"
        ],
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
        "cert_mode": "自动申请：节点端用本机 80 端口向 Let's Encrypt 申请并自动续期，80 端口要能从公网访问。证书名字用「客户端 SNI」，没填就用节点的连接地址，可以是公网 IP（证书约 6 天一换）或域名（约 90 天）。选自动申请即视为同意 Let's Encrypt 订户协议（https://letsencrypt.org/repository/）。手填：自己把证书放到节点机上，填路径。",
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "tls_settings.allow_insecure": "只进订阅；手填自签证书时才打开，自动申请的证书不需要。",
        "tls_settings.server_name": "只进订阅，告诉客户端握手用的域名。"
      },
      "note": "走 UDP；证书默认自动申请（需要 80 端口），也可以手填路径。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "udp"
        },
        {
          "path": "cert_mode",
          "label": "证书来源",
          "default": "acme",
          "option_labels": {
            "acme": "自动申请（Let's Encrypt）",
            "file": "手填证书路径"
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "obfs.type",
          "label": "混淆类型",
          "option_labels": {
            "": "不混淆"
          }
        },
        {
          "path": "obfs.password",
          "label": "混淆密码",
          "show_if": {
            "obfs.type": [
              "salamander"
            ]
          }
        },
        {
          "path": "bandwidth.up",
          "label": "上行带宽（Mbps）",
          "placeholder": "留空不限"
        },
        {
          "path": "bandwidth.down",
          "label": "下行带宽（Mbps）",
          "placeholder": "留空不限"
        },
        {
          "path": "udp_timeout",
          "label": "UDP 超时",
          "advanced": true,
          "placeholder": "留空按 5m"
        },
        {
          "path": "tls_settings.server_name",
          "label": "客户端 SNI",
          "placeholder": "example.com"
        },
        {
          "path": "tls_settings.allow_insecure",
          "label": "允许不安全证书",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          }
        }
      ]
    },
    {
      "node_type": "juicity",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_mode",
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "cert_mode",
        "cert_path",
        "key_path",
        "congestion_control"
      ],
      "enums": {
        "cert_mode": [
          "acme",
          "file"
        ],
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
        "cert_mode": "自动申请：节点端用本机 80 端口向 Let's Encrypt 申请并自动续期，80 端口要能从公网访问。证书名字用「客户端 SNI」，没填就用节点的连接地址，可以是公网 IP（证书约 6 天一换）或域名（约 90 天）。选自动申请即视为同意 Let's Encrypt 订户协议（https://letsencrypt.org/repository/）。手填：自己把证书放到节点机上，填路径。",
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      },
      "note": "走 UDP；证书默认自动申请（需要 80 端口），也可以手填路径。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "udp"
        },
        {
          "path": "cert_mode",
          "label": "证书来源",
          "default": "acme",
          "option_labels": {
            "acme": "自动申请（Let's Encrypt）",
            "file": "手填证书路径"
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "congestion_control",
          "label": "拥塞控制",
          "option_labels": {
            "": "默认"
          }
        }
      ]
    },
    {
      "node_type": "mieru",
      "version": 1,
      "status": "stable",
      "required": [],
      "allowed_properties": [
        "transport"
      ],
      "enums": {
        "transport": [
          "TCP",
          "UDP"
        ]
      },
      "note": "只有传输方式一项。",
      "fields": [
        {
          "path": "transport",
          "label": "传输方式",
          "default": "TCP"
        }
      ]
    },
    {
      "node_type": "naive",
      "version": 1,
      "status": "stable",
      "required": [
        "tls",
        "cert_mode",
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "tls",
        "cert_mode",
        "cert_path",
        "key_path",
        "security",
        "tls_settings.server_name",
        "tls_settings.allow_insecure",
        "fallback"
      ],
      "enums": {
        "cert_mode": [
          "acme",
          "file"
        ],
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
        "cert_mode": "自动申请：节点端用本机 80 端口向 Let's Encrypt 申请并自动续期，80 端口要能从公网访问。证书名字用「客户端 SNI」，没填就用节点的连接地址，可以是公网 IP（证书约 6 天一换）或域名（约 90 天）。选自动申请即视为同意 Let's Encrypt 订户协议（https://letsencrypt.org/repository/）。手填：自己把证书放到节点机上，填路径。",
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "fallback": "选填，host:port。回落目标是一个明文 HTTP 站点，认证失败的探测会被转过去，让节点看起来像个普通网站；可以填本机（如 127.0.0.1:80 的本机 nginx），不能填内网地址。留空时回一个中性的 404 页面。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "tls_settings.allow_insecure": "只进订阅；手填自签证书时才打开，自动申请的证书不需要。",
        "tls_settings.server_name": "只进订阅，告诉客户端握手用的域名。"
      },
      "note": "必须开 TLS；证书默认自动申请，也可以手填路径。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "tcp"
        },
        {
          "path": "tls",
          "label": "开启 TLS",
          "default": true
        },
        {
          "path": "cert_mode",
          "label": "证书来源",
          "default": "acme",
          "option_labels": {
            "acme": "自动申请（Let's Encrypt）",
            "file": "手填证书路径"
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "security",
          "label": "security",
          "advanced": true,
          "default": "none"
        },
        {
          "path": "tls_settings.server_name",
          "label": "客户端 SNI",
          "placeholder": "example.com"
        },
        {
          "path": "tls_settings.allow_insecure",
          "label": "允许不安全证书",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          }
        },
        {
          "path": "fallback",
          "label": "回落地址",
          "placeholder": "127.0.0.1:80"
        }
      ]
    },
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
        "chacha20-ietf-poly1305",
        "2022-blake3-aes-128-gcm",
        "2022-blake3-aes-256-gcm"
      ],
      "hints": {
        "cipher": "2022 开头的是 Shadowsocks 2022：服务端密钥由面板按节点生成，用户密钥跟随各自的订阅链接，不用填密码；Clash Premium、Surfboard、Loon、Stash 不支持，这几种订阅里会略过该节点。"
      },
      "note": "只有加密方式一项；用户密钥由面板按订阅生成。",
      "fields": [
        {
          "path": "cipher",
          "label": "加密方式",
          "default": "aes-256-gcm"
        }
      ]
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
      ],
      "hints": {
        "handshake_server": "被借用握手的真实站点。",
        "wildcard_sni": "打开后服务端用客户端给的 SNI 当握手目标，等于认证用户能让节点去连任意主机的 443；默认 off。"
      },
      "note": "借用一个真实站点的 TLS 握手，外面再套一层 Shadowsocks。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "tcp"
        },
        {
          "path": "version",
          "label": "版本",
          "default": 3
        },
        {
          "path": "password",
          "label": "密码"
        },
        {
          "path": "handshake_server",
          "label": "握手站点",
          "placeholder": "www.apple.com"
        },
        {
          "path": "server_port",
          "label": "握手端口",
          "placeholder": "443"
        },
        {
          "path": "server",
          "label": "内层服务地址",
          "advanced": true
        },
        {
          "path": "method",
          "label": "内层加密方式",
          "default": "aes-256-gcm"
        },
        {
          "path": "strict",
          "label": "严格模式",
          "default": false
        },
        {
          "path": "wildcard_sni",
          "label": "通配 SNI",
          "default": "off"
        }
      ]
    },
    {
      "node_type": "socks",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_mode"
      ],
      "allowed_properties": [
        "network",
        "tls",
        "cert_mode",
        "cert_path",
        "key_path",
        "security"
      ],
      "enums": {
        "cert_mode": [
          "acme",
          "file"
        ],
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
        "cert_mode": "自动申请：节点端用本机 80 端口向 Let's Encrypt 申请并自动续期，80 端口要能从公网访问。证书名字用「客户端 SNI」，没填就用节点的连接地址，可以是公网 IP（证书约 6 天一换）或域名（约 90 天）。选自动申请即视为同意 Let's Encrypt 订户协议（https://letsencrypt.org/repository/）。手填：自己把证书放到节点机上，填路径。",
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。"
      },
      "note": "开 TLS 时需要证书：默认自动申请，也可以手填路径。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式"
        },
        {
          "path": "tls",
          "label": "开启 TLS",
          "default": false
        },
        {
          "path": "cert_mode",
          "label": "证书来源",
          "show_if": {
            "tls": [
              "true"
            ]
          },
          "default": "acme",
          "option_labels": {
            "acme": "自动申请（Let's Encrypt）",
            "file": "手填证书路径"
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "cert_mode": [
              "file"
            ],
            "tls": [
              "true"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "cert_mode": [
              "file"
            ],
            "tls": [
              "true"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "security",
          "label": "security",
          "advanced": true,
          "default": "none"
        }
      ]
    },
    {
      "node_type": "trojan",
      "version": 1,
      "status": "stable",
      "required": [
        "tls",
        "cert_mode"
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
        "cert_mode",
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
        "cert_mode": [
          "acme",
          "file"
        ],
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
        "cert_mode": "自动申请：节点端用本机 80 端口向 Let's Encrypt 申请并自动续期，80 端口要能从公网访问。证书名字用「客户端 SNI」，没填就用节点的连接地址，可以是公网 IP（证书约 6 天一换）或域名（约 90 天）。选自动申请即视为同意 Let's Encrypt 订户协议（https://letsencrypt.org/repository/）。手填：自己把证书放到节点机上，填路径。",
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "fallback": "选填，host:port。回落目标是一个明文 HTTP 站点，认证失败的探测会被转过去，让节点看起来像个普通网站；可以填本机（如 127.0.0.1:80 的本机 nginx），不能填内网地址。留空时回一个中性的 404 页面。只在 tcp 传输上生效。",
        "grpc_path": "旧字段名，与 serviceName 二选一即可。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "mask": "把 UDP 包变成随机字节，盖掉 mKCP 自身的特征。",
        "reality_settings.dest": "借用握手的真实公网站点，域名:端口，例如 www.apple.com:443；dest 需支持 TLS 1.3 + X25519。不能填 IP、localhost 或内网域名。",
        "reality_settings.server_name": "可填多个，用逗号分隔；每个用户的订阅按固定规则分到其中一个，分散特征。",
        "reality_settings.short_id": "必填，可填多个（逗号分隔），每个不超过 16 位十六进制；每个用户的订阅分到其中一个。",
        "tls_settings.allow_insecure": "只进订阅；手填自签证书时才打开，自动申请的证书不需要。",
        "tls_settings.server_name": "只进订阅，告诉客户端握手用的域名。",
        "utls": "客户端模仿的浏览器 TLS 指纹，订阅三种格式都会下发；留空按 chrome。",
        "ws_path": "旧字段名，与「路径」二选一即可。"
      },
      "note": "缺省是普通 TLS（1）+ 自动申请证书（Let's Encrypt）：上线前把对外地址解析到这台服务器；也可选 REALITY（2）。Trojan 不能不加密；没有流控。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "tcp"
        },
        {
          "path": "tls",
          "label": "加密",
          "default": 1,
          "option_labels": {
            "1": "1 · TLS",
            "2": "2 · REALITY"
          }
        },
        {
          "path": "utls",
          "label": "uTLS 指纹",
          "option_labels": {
            "": "留空（chrome）"
          }
        },
        {
          "path": "cert_mode",
          "label": "证书来源",
          "show_if": {
            "tls": [
              "1"
            ]
          },
          "default": "acme",
          "option_labels": {
            "acme": "自动申请（Let's Encrypt）",
            "file": "手填证书路径"
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "cert_mode": [
              "file"
            ],
            "tls": [
              "1"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "cert_mode": [
              "file"
            ],
            "tls": [
              "1"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "tls_settings.server_name",
          "label": "客户端 SNI",
          "show_if": {
            "tls": [
              "1"
            ]
          },
          "placeholder": "example.com"
        },
        {
          "path": "tls_settings.allow_insecure",
          "label": "允许不安全证书",
          "show_if": {
            "cert_mode": [
              "file"
            ],
            "tls": [
              "1"
            ]
          }
        },
        {
          "path": "reality_settings.dest",
          "label": "REALITY 目标站（dest）",
          "show_if": {
            "tls": [
              "2"
            ]
          },
          "default": "www.apple.com:443",
          "placeholder": "www.apple.com:443"
        },
        {
          "path": "reality_settings.server_name",
          "label": "REALITY SNI",
          "show_if": {
            "tls": [
              "2"
            ]
          },
          "default": "www.apple.com",
          "placeholder": "www.apple.com"
        },
        {
          "path": "reality_settings.private_key",
          "label": "REALITY 私钥",
          "show_if": {
            "tls": [
              "2"
            ]
          }
        },
        {
          "path": "reality_settings.public_key",
          "label": "REALITY 公钥",
          "show_if": {
            "tls": [
              "2"
            ]
          }
        },
        {
          "path": "reality_settings.short_id",
          "label": "REALITY short_id",
          "show_if": {
            "tls": [
              "2"
            ]
          },
          "placeholder": "0a1b2c3d"
        },
        {
          "path": "network_settings.path",
          "label": "路径（path）",
          "show_if": {
            "network": [
              "ws",
              "httpupgrade"
            ]
          },
          "placeholder": "/ws"
        },
        {
          "path": "network_settings.headers.Host",
          "label": "Host 头",
          "show_if": {
            "network": [
              "ws",
              "httpupgrade",
              "grpc"
            ]
          },
          "placeholder": "cdn.example.com"
        },
        {
          "path": "network_settings.serviceName",
          "label": "gRPC serviceName",
          "show_if": {
            "network": [
              "grpc"
            ]
          },
          "placeholder": "grpc"
        },
        {
          "path": "network_settings.mode",
          "label": "XHTTP 模式",
          "show_if": {
            "network": []
          }
        },
        {
          "path": "ws_path",
          "label": "ws_path（兼容字段）",
          "advanced": true,
          "show_if": {
            "network": [
              "ws"
            ]
          }
        },
        {
          "path": "grpc_path",
          "label": "grpc_path（兼容字段）",
          "advanced": true,
          "show_if": {
            "network": [
              "grpc"
            ]
          }
        },
        {
          "path": "fallback",
          "label": "回落地址",
          "show_if": {
            "network": [
              "tcp",
              ""
            ]
          },
          "placeholder": "127.0.0.1:80"
        },
        {
          "path": "mask",
          "label": "mKCP 掩码",
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "none 或 mkcp-aes128gcm"
        },
        {
          "path": "mask_password",
          "label": "掩码密码",
          "show_if": {
            "mask": [
              "mkcp-aes128gcm"
            ],
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "mtu",
          "label": "MTU",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "tti",
          "label": "TTI（毫秒）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "uplink_capacity",
          "label": "上行容量（MB/s）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "留空按 5"
        },
        {
          "path": "downlink_capacity",
          "label": "下行容量（MB/s）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "留空按 20"
        },
        {
          "path": "congestion",
          "label": "拥塞控制",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "read_buffer_size",
          "label": "读缓冲（MB）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "write_buffer_size",
          "label": "写缓冲（MB）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        }
      ]
    },
    {
      "node_type": "tuic",
      "version": 1,
      "status": "stable",
      "required": [
        "cert_mode",
        "cert_path",
        "key_path"
      ],
      "allowed_properties": [
        "network",
        "cert_mode",
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
        "cert_mode": [
          "acme",
          "file"
        ],
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
        "cert_mode": "自动申请：节点端用本机 80 端口向 Let's Encrypt 申请并自动续期，80 端口要能从公网访问。证书名字用「客户端 SNI」，没填就用节点的连接地址，可以是公网 IP（证书约 6 天一换）或域名（约 90 天）。选自动申请即视为同意 Let's Encrypt 订户协议（https://letsencrypt.org/repository/）。手填：自己把证书放到节点机上，填路径。",
        "cert_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/fullchain.pem（见 docs/node-certificates.md）。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "tls_settings.allow_insecure": "只进订阅；手填自签证书时才打开，自动申请的证书不需要。",
        "tls_settings.server_name": "只进订阅，告诉客户端握手用的域名。"
      },
      "note": "走 UDP；证书默认自动申请（需要 80 端口），也可以手填路径。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "udp"
        },
        {
          "path": "cert_mode",
          "label": "证书来源",
          "default": "acme",
          "option_labels": {
            "acme": "自动申请（Let's Encrypt）",
            "file": "手填证书路径"
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "congestion_control",
          "label": "拥塞控制",
          "option_labels": {
            "": "留空（cubic）"
          }
        },
        {
          "path": "zero_rtt",
          "label": "0-RTT"
        },
        {
          "path": "auth_timeout",
          "label": "认证超时",
          "advanced": true,
          "placeholder": "留空按 3s"
        },
        {
          "path": "heartbeat",
          "label": "心跳间隔",
          "advanced": true,
          "placeholder": "留空按 10s"
        },
        {
          "path": "udp_timeout",
          "label": "UDP 超时",
          "advanced": true,
          "placeholder": "留空按 5m"
        },
        {
          "path": "tls_settings.server_name",
          "label": "客户端 SNI",
          "placeholder": "example.com"
        },
        {
          "path": "tls_settings.allow_insecure",
          "label": "允许不安全证书",
          "show_if": {
            "cert_mode": [
              "file"
            ]
          }
        }
      ]
    },
    {
      "node_type": "vless",
      "version": 1,
      "status": "stable",
      "required": [],
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
        "grpc_path": "旧字段名，与 serviceName 二选一即可。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "mask": "把 UDP 包变成随机字节，盖掉 mKCP 自身的特征。",
        "reality_settings.dest": "借用握手的真实公网站点，域名:端口，例如 www.apple.com:443；dest 需支持 TLS 1.3 + X25519。不能填 IP、localhost 或内网域名。",
        "reality_settings.server_name": "可填多个，用逗号分隔；每个用户的订阅按固定规则分到其中一个，分散特征。",
        "reality_settings.short_id": "必填，可填多个（逗号分隔），每个不超过 16 位十六进制；每个用户的订阅分到其中一个。",
        "tls": "缺省 2 · REALITY。选 0 · 不加密时传输不能是 tcp，否则保存会被拒绝。",
        "utls": "客户端模仿的浏览器 TLS 指纹，订阅三种格式都会下发；留空按 chrome。",
        "ws_path": "旧字段名，与「路径」二选一即可。"
      },
      "note": "缺省是 REALITY + tcp + xtls-rprx-vision：目标站与 SNI 已预填常用值（不懂就保持不动），密钥对进到这一步会自动生成；不加密时不能用裸 tcp（保存会被拒绝），要换 ws / httpupgrade / grpc / xhttp 并套 CDN。普通 TLS（tls=1）暂未开放。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式",
          "default": "tcp"
        },
        {
          "path": "tls",
          "label": "加密",
          "default": 2,
          "option_labels": {
            "0": "0 · 不加密",
            "2": "2 · REALITY"
          }
        },
        {
          "path": "flow",
          "label": "流控（flow）",
          "show_if": {
            "network": [
              "tcp",
              ""
            ],
            "tls": [
              "2"
            ]
          },
          "default": "xtls-rprx-vision",
          "option_labels": {
            "": "留空"
          }
        },
        {
          "path": "utls",
          "label": "uTLS 指纹",
          "show_if": {
            "tls": [
              "2"
            ]
          },
          "option_labels": {
            "": "留空（chrome）"
          }
        },
        {
          "path": "reality_settings.dest",
          "label": "REALITY 目标站（dest）",
          "show_if": {
            "tls": [
              "2"
            ]
          },
          "default": "www.apple.com:443",
          "placeholder": "www.apple.com:443"
        },
        {
          "path": "reality_settings.server_name",
          "label": "REALITY SNI",
          "show_if": {
            "tls": [
              "2"
            ]
          },
          "default": "www.apple.com",
          "placeholder": "www.apple.com"
        },
        {
          "path": "reality_settings.private_key",
          "label": "REALITY 私钥",
          "show_if": {
            "tls": [
              "2"
            ]
          }
        },
        {
          "path": "reality_settings.public_key",
          "label": "REALITY 公钥",
          "show_if": {
            "tls": [
              "2"
            ]
          }
        },
        {
          "path": "reality_settings.short_id",
          "label": "REALITY short_id",
          "show_if": {
            "tls": [
              "2"
            ]
          },
          "placeholder": "0a1b2c3d"
        },
        {
          "path": "network_settings.path",
          "label": "路径（path）",
          "show_if": {
            "network": [
              "ws",
              "httpupgrade",
              "xhttp",
              "xhttp-h3"
            ]
          },
          "placeholder": "/ws"
        },
        {
          "path": "network_settings.headers.Host",
          "label": "Host 头",
          "show_if": {
            "network": [
              "ws",
              "httpupgrade",
              "xhttp",
              "xhttp-h3",
              "grpc"
            ]
          },
          "placeholder": "cdn.example.com"
        },
        {
          "path": "network_settings.serviceName",
          "label": "gRPC serviceName",
          "show_if": {
            "network": [
              "grpc"
            ]
          },
          "placeholder": "grpc"
        },
        {
          "path": "network_settings.mode",
          "label": "XHTTP 模式",
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "ws_path",
          "label": "ws_path（兼容字段）",
          "advanced": true,
          "show_if": {
            "network": [
              "ws"
            ]
          }
        },
        {
          "path": "grpc_path",
          "label": "grpc_path（兼容字段）",
          "advanced": true,
          "show_if": {
            "network": [
              "grpc"
            ]
          }
        },
        {
          "path": "fallback",
          "label": "回落地址",
          "show_if": {
            "network": [
              "tcp",
              ""
            ]
          },
          "placeholder": "127.0.0.1:80"
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "tls": []
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "tls": []
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "mask",
          "label": "mKCP 掩码",
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "none 或 mkcp-aes128gcm"
        },
        {
          "path": "mask_password",
          "label": "掩码密码",
          "show_if": {
            "mask": [
              "mkcp-aes128gcm"
            ],
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "mtu",
          "label": "MTU",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "tti",
          "label": "TTI（毫秒）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "uplink_capacity",
          "label": "上行容量（MB/s）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "留空按 5"
        },
        {
          "path": "downlink_capacity",
          "label": "下行容量（MB/s）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "留空按 20"
        },
        {
          "path": "congestion",
          "label": "拥塞控制",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "read_buffer_size",
          "label": "读缓冲（MB）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "write_buffer_size",
          "label": "写缓冲（MB）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "headers",
          "label": "自定义响应头",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          },
          "placeholder": "{\"X-Foo\":\"bar\"}"
        },
        {
          "path": "sc_max_each_post_bytes",
          "label": "每次上行最大字节",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          },
          "placeholder": "1000000 或 \"500000-1000000\""
        },
        {
          "path": "sc_min_posts_interval_ms",
          "label": "上行最小间隔（毫秒）",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "sc_max_buffered_posts",
          "label": "最多缓存的上行包",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "sc_stream_up_server_secs",
          "label": "stream-up 保活秒数",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          },
          "placeholder": "\"20-80\""
        },
        {
          "path": "session_placement",
          "label": "session 位置",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "session_key",
          "label": "session 键名",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "seq_placement",
          "label": "seq 位置",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "seq_key",
          "label": "seq 键名",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "uplink_http_method",
          "label": "上行 HTTP 方法",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "uplink_data_placement",
          "label": "上行数据位置",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "uplink_data_key",
          "label": "上行数据键名",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "uplink_chunk_size",
          "label": "上行分块大小",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "server_max_header_bytes",
          "label": "服务端最大头部字节",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        }
      ]
    },
    {
      "node_type": "vmess",
      "version": 1,
      "status": "stable",
      "required": [],
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
        "grpc_path": "旧字段名，与 serviceName 二选一即可。",
        "key_path": "绝对路径，放在 /etc/pandora-native/certs/ 下，例如 /etc/pandora-native/certs/example.com/privkey.pem。",
        "mask": "把 UDP 包变成随机字节，盖掉 mKCP 自身的特征。",
        "network": "VMess 不能开 TLS：建议用 ws / httpupgrade / grpc / xhttp 并套 CDN 或 TLS 反代；裸 tcp 能保存，但特征明显、容易被识别封锁。",
        "tls": "VMess 不支持 REALITY，只有这一个选项。",
        "ws_path": "旧字段名，与「路径」二选一即可。"
      },
      "note": "VMess 不能开 TLS：建议用 ws / httpupgrade / grpc / xhttp 并套 CDN 或 TLS 反代；裸 tcp 能保存，但特征明显、容易被识别封锁。",
      "fields": [
        {
          "path": "network",
          "label": "传输方式"
        },
        {
          "path": "tls",
          "label": "加密",
          "default": 0,
          "option_labels": {
            "0": "0 · 不加密"
          }
        },
        {
          "path": "security",
          "label": "加密方式",
          "option_labels": {
            "": "默认"
          }
        },
        {
          "path": "utls",
          "label": "uTLS 指纹",
          "advanced": true,
          "option_labels": {
            "": "留空（chrome）"
          }
        },
        {
          "path": "network_settings.path",
          "label": "路径（path）",
          "show_if": {
            "network": [
              "ws",
              "httpupgrade",
              "xhttp",
              "xhttp-h3"
            ]
          },
          "placeholder": "/ws"
        },
        {
          "path": "network_settings.headers.Host",
          "label": "Host 头",
          "show_if": {
            "network": [
              "ws",
              "httpupgrade",
              "xhttp",
              "xhttp-h3",
              "grpc"
            ]
          },
          "placeholder": "cdn.example.com"
        },
        {
          "path": "network_settings.serviceName",
          "label": "gRPC serviceName",
          "show_if": {
            "network": [
              "grpc"
            ]
          },
          "placeholder": "grpc"
        },
        {
          "path": "network_settings.mode",
          "label": "XHTTP 模式",
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "ws_path",
          "label": "ws_path（兼容字段）",
          "advanced": true,
          "show_if": {
            "network": [
              "ws"
            ]
          }
        },
        {
          "path": "grpc_path",
          "label": "grpc_path（兼容字段）",
          "advanced": true,
          "show_if": {
            "network": [
              "grpc"
            ]
          }
        },
        {
          "path": "cert_path",
          "label": "证书路径",
          "show_if": {
            "tls": []
          },
          "placeholder": "/etc/pandora-native/certs/example.com/fullchain.pem"
        },
        {
          "path": "key_path",
          "label": "私钥路径",
          "show_if": {
            "tls": []
          },
          "placeholder": "/etc/pandora-native/certs/example.com/privkey.pem"
        },
        {
          "path": "mask",
          "label": "mKCP 掩码",
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "none 或 mkcp-aes128gcm"
        },
        {
          "path": "mask_password",
          "label": "掩码密码",
          "show_if": {
            "mask": [
              "mkcp-aes128gcm"
            ],
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "mtu",
          "label": "MTU",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "tti",
          "label": "TTI（毫秒）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "uplink_capacity",
          "label": "上行容量（MB/s）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "留空按 5"
        },
        {
          "path": "downlink_capacity",
          "label": "下行容量（MB/s）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          },
          "placeholder": "留空按 20"
        },
        {
          "path": "congestion",
          "label": "拥塞控制",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "read_buffer_size",
          "label": "读缓冲（MB）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "write_buffer_size",
          "label": "写缓冲（MB）",
          "advanced": true,
          "show_if": {
            "network": [
              "mkcp"
            ]
          }
        },
        {
          "path": "headers",
          "label": "自定义响应头",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          },
          "placeholder": "{\"X-Foo\":\"bar\"}"
        },
        {
          "path": "sc_max_each_post_bytes",
          "label": "每次上行最大字节",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          },
          "placeholder": "1000000 或 \"500000-1000000\""
        },
        {
          "path": "sc_min_posts_interval_ms",
          "label": "上行最小间隔（毫秒）",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "sc_max_buffered_posts",
          "label": "最多缓存的上行包",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "sc_stream_up_server_secs",
          "label": "stream-up 保活秒数",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          },
          "placeholder": "\"20-80\""
        },
        {
          "path": "session_placement",
          "label": "session 位置",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "session_key",
          "label": "session 键名",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "seq_placement",
          "label": "seq 位置",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "seq_key",
          "label": "seq 键名",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "uplink_http_method",
          "label": "上行 HTTP 方法",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "uplink_data_placement",
          "label": "上行数据位置",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "uplink_data_key",
          "label": "上行数据键名",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "uplink_chunk_size",
          "label": "上行分块大小",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        },
        {
          "path": "server_max_header_bytes",
          "label": "服务端最大头部字节",
          "advanced": true,
          "show_if": {
            "network": [
              "xhttp",
              "xhttp-h3"
            ]
          }
        }
      ]
    }
  ]
} as const
