/**
 * [INPUT]: 无运行时依赖；被 core/runtime.ts 的 hasContract 与 tests/api-surface.test.ts 读取
 * [OUTPUT]: 对外提供 PendingContract 类型、PendingContractSpec 类型、pendingContracts 登记表
 * [POS]: core 的“待接后端契约”唯一登记处：前端已实现、后端尚未提供的接口路径与权限码只能登记在这里，默认关闭；
 *        契约测试据此判定“前端调了后端没有的接口”是登记在案还是漂移
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
export type PendingContract =
  | "balance-operation-v1"
  | "refunds-be4"
  | "reconcile-be4"
  | "route-groups-v1"
  | "coupon-edit-v1"
  | "mail-template-preview-v1";

export type PendingContractSpec = {
  /** 归一化路径：路径参数与模板表达式写成 {}，不含查询串 */
  paths: string[];
  /** 后端权限字典（migrations 里 INSERT INTO permissions）尚不存在的权限码 */
  permissions?: string[];
  /** 依据的后端契约或工作包，以及现行后端实际提供的替代 */
  note: string;
};

// 后端补齐某项契约后：删掉这里的条目，并在入口 HTML 的 pandora-contracts 里打开开关。
// 契约测试会同时拒绝“登记了但后端已存在”和“调用了但未登记”两种漂移。
export const pendingContracts: Record<PendingContract, PendingContractSpec> = {
  "balance-operation-v1": {
    paths: ["v1/balance-adjustments/{}"],
    note: "BE1：POST users/{id}/balance 接受 operation_id 与十进制 amount_minor，并按操作号回查结果。现行后端只接受整数 amount。",
  },
  "refunds-be4": {
    paths: [
      "v1/orders/{}/refund-preview",
      "v1/orders/{}/refunds",
      "v1/refunds",
      "v1/refunds/{}",
      "v1/refunds/{}/{}",
      "v1/refund-review-cases",
    ],
    permissions: ["billing.refund.execute"],
    note: "BE4-B1/B2：退款预览、按来源申请、审批、执行入队、外部凭据登记与待核对队列。现行后端没有退款写路径。",
  },
  "reconcile-be4": {
    paths: ["v1/orders/{}/reconcile", "v1/orders/{}/reconciliation-jobs"],
    note: "BE4：主动向支付渠道查单与查单任务队列。现行后端只有支付回调与 orders/{id}/payments 只读记录。",
  },
  "route-groups-v1": {
    paths: ["v1/route-groups", "v1/route-groups/{}"],
    note: "版本化共享路由组资源。现行后端只有逐节点 nodes/{id}/routing。",
  },
  "coupon-edit-v1": {
    paths: ["v1/coupons/{}"],
    note: "优惠券编辑（POST coupons/{id} 带 expected_updated_at）。现行后端只有创建、批量生成与启停。",
  },
  "mail-template-preview-v1": {
    paths: ["v1/mail/templates/preview"],
    note: "通知模板草稿预览。现行后端只有 mail/templates/test 实发测试信。",
  },
};
