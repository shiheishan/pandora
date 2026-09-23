# 第一轮前端验证记录

日期：2026-09-05。范围：本目录新 React 工程，未提交、未部署、未操作生产服务。

| 检查                                     | 结果       | 证据与边界                                                                                                      |
| ---------------------------------------- | ---------- | --------------------------------------------------------------------------------------------------------------- |
| TypeScript strict 检查                   | PASS       | `npm run typecheck`；包含 noUncheckedIndexedAccess、未使用符号检查                                              |
| 完整自动化测试                           | PASS       | `npm test`，2026-09-05 13:00:26 开始，7文件、37测试全部通过，8.18秒；模拟请求及虚构数据                         |
| 双入口生产构建                           | PASS       | 随后 `npm run build`；strict TypeScript通过，Admin/Portal均输出独立index与完整动态资源；主入口index-BeRsQXH7.js |
| 首包大小                                 | PARTIAL    | 入口52.22KB + 共享414.34KB gzip，另有CSS/小型runtime；Vite仍有>500KB未压缩chunk提示，未抬阈值隐藏               |
| 原版全部操作迁移                         | PARTIAL    | 23+9导航有真实页面，高级操作边界见README；不是全功能覆盖率                                                      |
| 最新构建真实浏览器走查                   | UNVERIFIED | 主代理另有第一份旧构建的浏览器夹具记录，不能自动套用到本构建                                                    |
| 当前API +真实可丢弃数据库的前端闭环      | UNVERIFIED | 后端PG测试与前端模拟测试分别记录，不能拼成未经执行的E2E                                                         |
| 生产支付/退款、节点数据面/计费、生产前缀 | UNVERIFIED | 本工程未连接生产写入或执行目标节点命令                                                                          |
| 前后端接口面契约（2026-09-21 追加）      | NOT RUN    | 新增 `tests/api-surface.test.ts` 对照两个网关路由与权限字典；6 项待接契约登记在 `src/core/contracts.ts` 并默认关闭。追加当天本机没有 Node 与 Go 工具链，`npm test` 未执行；用 Python 复现同一扫描算法，未登记的路径与权限码为 0 |
| 旧页迁移清单与 /app 嵌入（2026-09-22 追加） | PARTIAL | 同日晚本机 Node 22.23.2 实跑：`npm ci` 0 漏洞；`npm run typecheck` 与双 mode 构建通过；`tests/api-surface.test.ts` 8 项 PASS（旧页迁移清单管理端 27、门户 13 条与实际差集一致）——此前该文件在 jsdom 下因 `new URL(".", import.meta.url)` 被 `fileURLToPath` 拒绝而整体未加载，本次改为传字符串；`make frontend-embed` 后 Go `web/app_test.go` 真实产物形态 PASS。全量 vitest 40 文件 156 项中 1 项 FAIL：`node-pools` 删除失败提示 `toBeVisible` 首轮通过、之后连续失败，根因 UNKNOWN。另删去 `manual-order` 中假设旧页写恢复记录的用例（旧页人工开单不写恢复记录、没有被测的三个函数），React 自身恢复路径仍由该文件首个用例覆盖。登录页“返回原版入口”渲染效果未看过（UNVERIFIED） |

自动化覆盖：精确金额与溢出、随机反代前缀、真实Admin/me没有email、稳定主体优先级、重认证401与普通401/403边界、成功HTTP但错误响应体、过期账户迟到结果、缺失列表字段、同操作双提交/503固定ID/恢复/404未知、弹窗Escape/卸载/路由关闭一次性结算、StrictMode表单错误保留、点击+Enter同步锁、关闭重认证后迟到响应不得设置token、主体/实体草稿隔离、交错列表请求不越路由、SSE批量失效/取消/账户变更、节点服务显隐使用正式batch API、TLS按Schema枚举与嵌套私钥标记。Portal新增工单200但缺ID时保持正文、不错误导航，已有专用组件回归。

退款测试覆盖：按原payment/balance来源精确分配，不自动替管理员分配；撤销订阅须全额退回剩余资金；服务端冻结双人策略、字段缺失保守处理；稳定申请/审批action与版本、跨主体隔离、损坏/不可用会话存储、503/错误200/404仍未知；重复点击只发一个请求；审批未知GET不能伪造动作完成；执行202只入队，HTTP回执丢失后查询approved并显式重放同action/同payload，渠道unknown不能再次自动执行；外部证据只匹配原payment分腿的固定金额/币种，严格SHA256；本机文件已知SHA256计算且没有上传请求；必须勾选已核实渠道退款；503后保留原文与凭据，恢复只调用record-external-result，不调用execute；渠道已退款但本地未入账单独展示，允许原凭据重试本地结算。

调试历史没有算作通过：初次新增入队恢复组件测试使用了错误的通用按钮名称，出现1失败/36通过；之后将金融确认按钮明确改为“确认提交到退款队列”并按实际可访问名称断言，完整37项再跑全部通过。早期24、32、36项通过仅对应各自时点，最终以本次37项为准。定向测试未执行的项目不算PASS。

构建指纹（SHA256；全部36个构建文件逐项见 `BUILD-SHA256SUMS.txt`）：

| 文件                                   | SHA256                                                           |
| -------------------------------------- | ---------------------------------------------------------------- |
| dist/admin/index.html                  | ec8dd6ef9a6352a4a60a313aa15495beff14e55d162a54d103c919e8c2d4d065 |
| dist/portal/index.html                 | 1a4170de48ec2f9dc5b6dc6580595b6632742a9cc832a66d43be820dc31cb824 |
| assets/index-BeRsQXH7.js（两入口一致） | 3adda38189100d3a8ed306c6eb1abb2d11dda18356f216d824164518fc2421b4 |
| assets/Orders-YELgDuhk.js（含退款）    | bdaba2d7e8049e94b5adc1edc5ea48d3f87a9caf108c550ea1d33a2834e93eaf |

BE1 与 BE4-B1/B2 后端不在本仓库；对应入口自 2026-09-21 起登记为待接契约并默认关闭，前端可与现行后端一起构建。具体数据库、worker与真实渠道证据由主任务单独记录。EPay没有自动退款适配，复杂权益/佣金继续人工处理；本记录不宣称生产退款成功、实时DB闭环或新构建浏览器走查已经通过。复制时必须复制整个dist/admin和dist/portal，不能只替换index.html。
