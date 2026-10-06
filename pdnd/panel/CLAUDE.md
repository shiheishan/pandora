# pdnd/panel/
> L2 | 父级: /pdnd/CLAUDE.md

节点端与面板之间的通信层，只管协议与线格式，不做编排（编排在 node/）
  - 两条并列通道：兼容通道 Client 走 UniProxy（Xboard / V2board 兼容，bearer 鉴权），签名通道 SignedClient 走 /v1/nodes/*（Ed25519 请求签名 + 配置验签）
  - 用户列表与流量上报只有兼容通道；签名通道承载配置、心跳、生效回执与配置签名密钥轮换
  - 状态归属：配置 ETag 与用户 ETag 都存在 Client 里，何时作废由 node/ 决定（入站重建即 ForgetUsersVersion，配置应用失败且节点已停即 ForgetConfigVersion）
  - 签名通道没有 ETag，每轮全量拉取；「哪个版本已知装不上」的台账在 node/signed_config.go，不在本层
  - SSE 事件流只是加速通路：失败一律退避重连、不向上冒泡，轮询永远在

成员清单
client.go: 兼容通道 Client。Config / Users 各带一份 ETag，只在解析成功后才记，304 时返回 changed=false 而不是空列表；SetUsersVersion 接收事件流推来的版本，ForgetUsersVersion 在内核用户表被清空时作废，ForgetConfigVersion 在节点已停时作废配置 ETag 以便重试；Push / Alive 上报流量与在线 IP；streamWait 是 stream.go 重连等待的测试钩子，生产为 nil
stream.go: 面板 SSE 订阅。Stream 自管重连，退避 1 秒起翻倍到 30 秒封顶、带抖动，一次健康连接（回 200 且至少读到一帧，心跳也算）之后复位到 1 秒，只回 200 就断的不算；parseStreamEvent 认 sync.users / sync.user.delta / sync.config 三种事件，不认识的跳过而不断流
signed.go: 签名通道。Identity 落盘格式、LoadIdentity / SaveIdentity、SignedClient 的 Heartbeat / Config / ReportConfig / ReportEffectiveConfig / VerifyConfig / Do，请求逐个 Ed25519 签名；Do 对非 2xx 返回 StatusError（带状态码，文案不变），让 node/ 区分面板拒收与没送到；服务器地址拒绝远端明文 HTTP 与内嵌凭据，客户端不跟随任何重定向
enrollment.go: 两阶段节点接入（begin → status → commit），本地日志先落盘再发请求、可续跑，提交后才把身份提升为 signed.go 读的 identity.json；首装产生身份的唯一入口
effective_release.go: 生效发布版本契约 aegis-node-effective-config-release-v1 的签名原像与验签：钉住配置公钥 key_id、内容与来源清单 SHA-256、签发与过期窗口
config_key_transition.go: 配置签名密钥轮换。RefreshConfigSigningKey 取旧钥签发的过渡声明，校验身份、签名与 15 分钟投递窗口后先落盘再换内存中的钉扎公钥
heartbeat_metrics.go: 签名心跳的资源指标。复用 status.go 的采样，加 /proc 的负载、网络累计、TCP 连接数、开机时长，换算成面板 nodefabric.Metrics 的整数口径并钳到库列范围；HostCapacity 也供 enrollment begin
status.go: 兼容通道 /status 的运行状态采集与上报（CPU、内存按 MemAvailable、磁盘，字节口径），只报机器级指标不带任何用户信息
status_linux.go: go:build linux，statfs 按 Bavail 算根分区已用
status_other.go: go:build !linux，磁盘指标报零，只为开发机能编译与跑测试
*_test.go: users_etag_test.go 守 304 不等于清空、解析失败不记 ETag；stream_test.go 守 SSE 帧解析、增量版本区间、未知事件跳过与退避；stream_backoff_test.go 替换等待函数逐档核对退避的翻倍与复位；signed_test.go / auth_test.go 守签名规范请求、key_id 钉扎、令牌不进 URL、远端明文拒绝；enrollment_test.go、effective_release_test.go、config_key_transition_test.go、heartbeat_metrics_test.go、status_test.go 各守同名文件的契约

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
