# pdnd/node/
> L2 | 父级: /pdnd/CLAUDE.md

面板与内核之间的闭环，节点端唯一的业务编排层
  - 向上只认 panel/ 的两个客户端：兼容通道 Client（UniProxy，用户列表与流量上报只走这条）与签名通道 SignedClient（配置与心跳在它存在时优先走它）
  - 向下只认 core.Core 抽象，不知道背后是 NativeCore 还是 compat 构建的兼容内核
  - 单 goroutine 主循环：轮询节拍、上报节拍、状态节拍与 SSE 事件在同一个 select 里串行处理，用户镜像因此不加锁
  - 用户镜像不变式：n.known、n.userVersion、客户端用户 ETag 三份说的是同一件事「内核里已是这一版」，内核用户表被清空（入站重建、回滚失败）时只能经 resetUserMirror 一起作废，下一次拉用户必然是无条件全量

成员清单
node.go: Node 主循环与编排（签名配置台账在 signed_config.go）。syncOnce = 拉配置后紧接着拉用户（轮询与 sync.config 事件共用）；applyConfig 先快照、失败按 ConfigApplyError.PreviousPreserved 选择只补用户或整版回滚，回滚成功即恢复 started（此前回滚失败停摆的节点也算重新在服务），回滚也失败则 markInboundLost；兼容通道上应用失败且节点已停（首次未装上或回滚失败）时作废配置 ETag，下一轮重拉重试，旧配置仍在服务时不重试；applyUsers 以全量算 diff 落内核，applyUserDelta 只在 FromVersion 对得上时打补丁，否则回落全量；report 上报流量与在线 IP，reportStatus 发签名心跳或兼容 /status
signed_config.go: 签名通道配置台账。syncSignedConfig 拉取验签后按版本身份分流：版本身份 signedConfigKeyOf 只认内容（生效发布 release_id + generation + content_sha256，旧式 version + hash），不认每轮都变的 issued_at / 签名；已应用的只补报 switched / health_passed；最近一个装不上的版本记在 failedSigned（进程内存、单槽），旧配置仍在服务（回滚成功或 PreviousPreserved）时不再试装、返回 nil 让用户照常同步，节点已停（首装失败、回滚失败、刚重启）时每轮按拉取间隔重试；failed 每版本只报一次，送不到（传输错误、5xx、408、429）随后续拉取补报，面板明确拒收（其余 4xx，经 panel.StatusError 区分）即作罢；detail 截到面板上限 2048 字节；health_passed 要等稳定窗口过后且内核 InboundReadiness 就绪
*_test.go: config_rollback_test.go 守回滚（恢复旧入站与用户、回滚失败即停、PreviousPreserved 不重装）与生效健康窗口；user_resync_test.go 用会清空用户表的内核夹具与只认 ETag 的假面板守用户镜像不变式（纯轮询改配置、事件流改配置、重建后旧基准增量、回滚失败）以及节点停摆后兼容通道的配置重试；protocol_switch_test.go 守协议跟随面板；routing_test.go 守分流解析的缺省与显式空；status_report_test.go 守签名心跳携带 metrics；signed_failure_test.go 用按生效发布契约每轮重签的假面板与按端口拒绝的两种内核守签名通道坏版本只应用一次、failed 只报一次、新版本照常应用、节点停摆逐轮重试、回滚恢复后不再重试、失败上报补报不重装；signed_config_test.go 守版本身份不受重签影响、detail 截断与上报了结判定

法则: 成员完整·一行一文件·父级链接·技术词前置
