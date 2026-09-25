# panel/frontend/src/portal/screens/checkout/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

确认订单（用户门户-03-选购套餐.dc.html 的结账部分；契约门户-03、门户-02 的续费、外壳的支付弹窗；5.A D-E-1 / D-E-2）。地址决定模式：?pack= 流量包；?renew= 续费；?plan= 时没有生效订阅走新购、同套餐走续费、换套餐走变更套餐。
设计稿把「余额」当四选一的支付方式，后端是抵扣金额：改为「使用余额抵扣」开关（use_balance = min(余额, 优惠后应付)），抵扣后应付为 0 时隐藏支付方式、按钮改「确认支付」；USDT 删除。变更套餐的折算、优惠与退余额全用服务端试算（change-plan/preview），前端不自己算；续费遇改价（renewal_price.available=false）只列当前价格并在预览里标「原价格已调整」。
一次用户意图一个幂等键：按请求体指纹复用，双击、失败重试、关掉支付弹窗后再点都回放同一张订单，改了任何参数才换新键。下单后交给 common/PayFlow 的支付弹窗。

成员清单
index.tsx: 页面组件——载入目录与订阅、按模式建表单（整块按模式与目标重建），周期 / 容量单选、优惠码（试算失败显示后端原文、不带进下单）、余额开关、支付方式、订单预览与提交；异常地址、停售、不可续费各有空状态；续费 / 变更互斥 409 引导去订单页
model.ts: 纯逻辑——resolveMode、periodOptions、defaultPriceId、isRepriced、buildQuote、orderRequest（四种下单请求体，可选字段不用不传）、couponPreviewBody、couponNote、normalizeCoupon
Checkout.module.css: 页面样式，取自设计稿门户-03 结账部分
checkout.test.ts: 第 ② 步的单元测试（结账模式、改价、预览、请求体、优惠码、目录文案、选购页入口、支付回跳地址与成功文案）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
