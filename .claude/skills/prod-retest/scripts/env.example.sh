# 复测一轮的现场参数模板。复制到 ops-local/<轮次>/env.sh（被 git 忽略）再填真实值，本文件只放占位符。
# 本 skill 的本机脚本都以第一个参数接收这个文件：start.sh / peek.sh / pull.sh / push-scripts.sh / report.sh。

# ssh 别名（~/.ssh/config 与 ~/ai/servers/ 里登记过的名字）
PANEL_HOST=vultr-sgp-pt-panelN
LOADGEN_HOST=vultr-sgp-pt-loadgenN

# 面板对外的域名或公网 IPv4（没有域名就填 IP，面板走 https://<公网IP> 加 IP 证书）
PANEL_DOMAIN='<PANEL_DOMAIN>'

# 只用于报告打码与 nginx-realip，真实值只放在 ops-local
PANEL_IP='<PANEL_IP>'
LOADGEN_IP='<LOADGEN_IP>'
NODE1_IP=
NODE2_IP=

# 管理员邮箱用虚构域；口令与后台前缀不写在这里，只在下面两个 0600 文件里
ADMIN_EMAIL=ltadmin@example.com
ADMIN_CRED_FILE='ops-local/<轮次>/admin-cred.txt'   # 第 1 行邮箱，第 2 行口令
ADMIN_PATH_FILE='ops-local/<轮次>/admin-path.txt'   # 只有后台前缀一行

# 本机结果目录（场景子目录建在它下面）与对比基线场景目录
LOCAL_DIR='ops-local/<轮次>'
BASELINE_DIR=
