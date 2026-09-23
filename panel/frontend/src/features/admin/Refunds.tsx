/**
 * [INPUT]: 依赖 core/auth、core/dialogs、core/drafts 的 useDraft、core/api 的 failure、core/data、core/numbers；依赖 core/refunds 的 RefundCommand、退款状态判定与 zod Schema；依赖 @tanstack/react-query 与 react-router 的 useSearchParams
 * [OUTPUT]: 对外提供 RefundWorkspace 组件
 * [POS]: features/admin 的退款工作台：预览、按来源申请、审批、执行入队、外部凭据登记；整页按 BE4-B1/B2 契约编写，后端尚未提供，只在待接契约 refunds-be4 打开时由 Orders 挂载；审批权限码用后端字典里的 billing.refund.approve
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useRef, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Collapse,
  Form,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { z } from "zod";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { useDraft } from "../../core/drafts";
import { failure } from "../../core/api";
import { enc, principalSubject, text, type Row } from "../../core/data";
import { money, parseMinor } from "../../core/numbers";
import {
  RefundCommand,
  canApproveRefund,
  canExecuteRefund,
  canRecordRefundEvidence,
  hashRefundEvidence,
  refundFinancialState,
  refundResultMessage,
  entitlementActions,
  refundPolicyLabels,
  refundPreviewSchema,
  refundReason,
  refundSchema,
  validateRefundAllocation,
  type Refund,
  type RefundPreview,
} from "../../core/refunds";
import {
  Details,
  QueryPanel,
  Status,
  Unavailable,
} from "../../components/common";

const listSchema = z.object({
  items: z.array(refundSchema),
  limit: z.number(),
});
const stateLabels: Record<string, string> = {
  frozen: "尚未确认创建",
  unknown: "结果待核实",
  confirmed: "已收到操作结果",
  rejected: "请求被明确拒绝",
};
const commandLabels = {
  request: "退款申请",
  review: "审批动作",
  execute: "执行入队",
  external: "外部凭据登记",
};
const commandPermissions = {
  request: "billing.refund.request",
  review: "billing.refund.approve",
  execute: "billing.refund.execute",
  external: "billing.refund.execute",
};
function useActiveView(key: string) {
  const mounted = useRef(true);
  const current = useRef(key);
  current.current = key;
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return () => mounted.current && current.current === key;
}

export function RefundWorkspace({ orderId }: { orderId: string }) {
  const { api, principal, scope, can } = useAuth();
  const subject = principalSubject(principal);
  const isActive = useActiveView(`${scope}:${orderId}`);
  const client = useQueryClient();
  const [params, setParams] = useSearchParams();
  const selected = params.get("refund") || "";
  const [creating, setCreating] = useState(false);
  const [evidence, setEvidence] = useState<{ refund: Refund; legId: string }>();
  const [revision, setRevision] = useState(0);
  const [queueChecks, setQueueChecks] = useState<
    Record<string, Refund | undefined>
  >({});
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const { message } = App.useApp();
  const open = useDialog();
  const read = can("billing.ledger.read");
  const query = useQuery({
    queryKey: ["admin", scope, "refunds", "list"],
    queryFn: ({ signal }) => api.request("v1/refunds", listSchema, { signal }),
    enabled: read,
    retry: false,
  });
  const detail = useQuery({
    queryKey: ["admin", scope, "refunds", "detail", orderId, selected],
    queryFn: ({ signal }) =>
      api.request(
        `v1/refunds/${enc(selected)}`,
        refundSchema.refine(
          (value) => value.order_id === orderId,
          "此退款不属于当前订单",
        ),
        { signal },
      ),
    enabled: read && Boolean(selected),
    retry: false,
    refetchInterval: (query) =>
      query.state.data?.status === "processing" ? 5000 : false,
  });
  void revision;
  const saved = RefundCommand.restore(subject).filter(
    (command) => command.saved.orderId === orderId,
  );
  const unresolved = saved.filter((command) =>
    ["frozen", "unknown"].includes(command.saved.state),
  );
  const choose = (id: string) => {
    const next = new URLSearchParams(params);
    next.set("refund", id);
    setParams(next);
  };
  const refresh = async () => {
    setRevision((value) => value + 1);
    await client.invalidateQueries({ queryKey: ["admin", scope, "refunds"] });
  };
  const run = async (task: () => Promise<void>) => {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    try {
      await task();
    } catch (error) {
      if (isActive()) message.error(failure(error).message);
    } finally {
      lock.current = false;
      if (isActive()) {
        setBusy(false);
        setRevision((value) => value + 1);
      }
    }
  };
  const showResult = async (result: Refund) => {
    if (!isActive()) return;
    setCreating(false);
    setEvidence(undefined);
    choose(result.operation_id);
    client.setQueryData(
      ["admin", scope, "refunds", "detail", orderId, result.operation_id],
      result,
    );
    await refresh();
    await Promise.all(
      ["orders", "users", "subscriptions"].map((key) =>
        client.invalidateQueries({ queryKey: ["admin", scope, key] }),
      ),
    );
  };
  const execute = (refund: Refund) => {
    let command: RefundCommand | undefined;
    return open({
      title: "将批准的退款交给处理队列",
      submitLabel: "确认提交到退款队列",
      danger: true,
      description: (
        <>
          <p>
            {money(refund.amount_minor, refund.currency)} ·{" "}
            {refundPolicyLabels[refund.entitlement_action]}。
          </p>
          <p>
            提交后由后台处理。进入队列不代表退款完成；EPay等不支持自动退款的渠道会转人工核对。结果不明时只能查询，不能再次自动退款。
          </p>
        </>
      ),
      fields: [],
      onSubmit: async () => {
        command ||= RefundCommand.execute(subject, refund);
        try {
          const result = await command.send(api);
          await showResult(result);
          if (isActive()) message.info(refundResultMessage(result));
        } finally {
          if (isActive()) setRevision((value) => value + 1);
        }
      },
    });
  };
  const review = (refund: Refund, decision: "approve" | "reject") => {
    let command: RefundCommand | undefined;
    return open({
      submitLabel:
        decision === "approve"
          ? "确认批准申请"
          : refund.requested_by === subject
            ? "确认撤回申请"
            : "确认驳回申请",
      title:
        decision === "approve"
          ? "批准退款申请"
          : refund.requested_by === subject
            ? "撤回自己的退款申请"
            : "驳回退款申请",
      danger: true,
      description: (
        <>
          <p>
            申请金额 {money(refund.amount_minor, refund.currency)}；
            {refundPolicyLabels[refund.entitlement_action]}。
          </p>
          <p>
            批准只允许进入后续处理阶段，不代表渠道已退款或权益已撤销。版本{" "}
            {refund.version}。
          </p>
        </>
      ),
      fields: [
        {
          name: "reason",
          label: "审批原因",
          required: true,
          type: "textarea",
          help: "5至2000个字符，记录实际审批人。",
        },
      ],
      onSubmit: async (values) => {
        if (!command)
          command = RefundCommand.review(
            subject,
            refund,
            decision,
            text(values.reason, "").trim(),
          );
        else if (
          command.saved.kind === "review" &&
          command.saved.payload.reason !== text(values.reason, "").trim()
        )
          throw new Error(
            "此审批意图已冻结，请保留原理由；失败结果可在恢复记录中核对",
          );
        try {
          const result = await command.send(api);
          await showResult(result);
          message.success(
            decision === "approve"
              ? "审批结果已确认，尚未执行退款"
              : "拒绝或撤回结果已确认",
          );
        } finally {
          setRevision((value) => value + 1);
        }
      },
    });
  };
  const current = detail.data;
  return (
    <div className="refund-workspace">
      <Alert
        className="mb"
        type="info"
        showIcon
        title="退款按原收款来源分配，申请、审批、执行分别确认"
        description="保留权益与撤销订单订阅必须明确选择。执行只进入队列；渠道已退款与本地账本已入账分别展示。渠道结果不明时不能再次自动退款。"
      />
      <Space className="mb" wrap>
        {can("billing.refund.request") && (
          <Button
            type="primary"
            disabled={creating || unresolved.length > 0}
            onClick={() => setCreating(true)}
          >
            发起退款申请
          </Button>
        )}
        <Button
          loading={busy || query.isFetching}
          onClick={() => void run(refresh)}
        >
          刷新退款状态
        </Button>
      </Space>
      {unresolved.length > 0 && (
        <Alert
          className="mb"
          type="warning"
          title="本订单存在尚未核实的资金意图，暂不创建另一笔退款申请"
          description="查询404或网络失败不能证明原请求未发生。入队回执不明时先查询，仍为已批准才能按原操作重试入队；渠道结果不明不能再次自动退款。凭据登记失败可用原参数重试本地结算。"
        />
      )}
      {saved.length > 0 && (
        <Card title="此标签页的资金恢复记录" size="small" className="mb">
          {saved.map((command) => (
            <div className="refund-recovery mb" key={command.saved.id}>
              <Space wrap>
                <Tag>{commandLabels[command.saved.kind]}</Tag>
                <Typography.Text copyable>{command.saved.id}</Typography.Text>
                <Tag>{stateLabels[command.saved.state]}</Tag>
              </Space>
              <Typography.Paragraph type="secondary">
                退款号 {command.saved.operationId}
                {command.saved.kind === "request"
                  ? ` · ${money(command.saved.payload.amount_minor, command.saved.currency)} · ${refundPolicyLabels[command.saved.payload.entitlement_action]}`
                  : ` · 冻结版本 ${command.saved.payload.expected_version}`}
              </Typography.Paragraph>
              {command.saved.error && (
                <Typography.Paragraph type="danger">
                  {command.saved.error}
                </Typography.Paragraph>
              )}
              <Space wrap>
                {read && (
                  <Button
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        const result = await command.query(api);
                        if (isActive() && command.saved.kind === "execute")
                          setQueueChecks((values) => ({
                            ...values,
                            [command.saved.id]:
                              result.status === "approved" ? result : undefined,
                          }));
                        await showResult(result);
                        if (
                          command.saved.kind !== "request" &&
                          command.saved.state !== "confirmed"
                        )
                          message.info(
                            command.saved.kind === "execute"
                              ? result.status === "approved"
                                ? "当前仍为已批准，可按原操作核对或重试入队；这不会创建第二笔渠道退款"
                                : "已读取当前退款状态，继续核对渠道和账本；不重新执行退款"
                              : "已读取当前退款状态；动作回执仍未确认，可保留原参数核对",
                          );
                      })
                    }
                  >
                    按退款号核对
                  </Button>
                )}
                {(["frozen", "unknown"].includes(command.saved.state) ||
                  (command.saved.kind === "external" &&
                    command.saved.state === "confirmed" &&
                    detail.data?.operation_id === command.saved.operationId &&
                    detail.data.legs.some(
                      (leg) =>
                        leg.id ===
                          (command.saved.kind === "external"
                            ? command.saved.payload.leg_id
                            : "") &&
                        leg.financial_state === "provider_succeeded",
                    ))) &&
                  (command.saved.kind !== "execute" ||
                    command.saved.state === "frozen" ||
                    queueChecks[command.saved.id]?.status === "approved") &&
                  can(commandPermissions[command.saved.kind]) && (
                    <Button
                      disabled={busy}
                      onClick={() =>
                        void run(async () => {
                          const checked = queueChecks[command.saved.id];
                          setQueueChecks((values) => ({
                            ...values,
                            [command.saved.id]: undefined,
                          }));
                          const result = await command.send(api, checked);
                          await showResult(result);
                          if (isActive())
                            message.info(refundResultMessage(result));
                        })
                      }
                    >
                      {command.saved.kind === "external"
                        ? "原凭据重试本地结算"
                        : command.saved.kind === "execute"
                          ? command.saved.state === "frozen"
                            ? "提交尚未发送的执行请求"
                            : "按原操作核对或重试入队"
                          : "重放原参数"}
                    </Button>
                  )}
                {command.saved.state === "rejected" && (
                  <Button
                    disabled={busy}
                    onClick={() => {
                      command.discardRejected();
                      setRevision((value) => value + 1);
                    }}
                  >
                    移除明确拒绝的记录
                  </Button>
                )}
              </Space>
            </div>
          ))}
        </Card>
      )}
      {creating && (
        <RefundRequestForm
          orderId={orderId}
          onClose={() => setCreating(false)}
          onSaved={() => setRevision((value) => value + 1)}
          onResult={showResult}
        />
      )}
      {evidence && (
        <RefundEvidenceForm
          key={`${evidence.refund.operation_id}:${evidence.legId}`}
          refund={evidence.refund}
          legId={evidence.legId}
          onClose={() => setEvidence(undefined)}
          onSaved={() => setRevision((value) => value + 1)}
          onResult={showResult}
        />
      )}
      {read ? (
        <QueryPanel query={query}>
          <Card title="此订单的退款申请" className="mb">
            <Typography.Paragraph type="secondary">
              列表从全租户最近 {query.data?.limit || 100}{" "}
              笔中筛选，可能不含较早申请；可通过下方退款号单独查询。
            </Typography.Paragraph>
            <Input.Search
              aria-label="按退款操作号查询"
              placeholder="输入完整退款操作号 UUID"
              enterButton="查询退款"
              onSearch={(value) => {
                const result = z.uuid().safeParse(value.trim());
                if (!result.success) {
                  message.error("请输入有效的退款操作号");
                  return;
                }
                choose(result.data);
              }}
              className="mb"
            />
            <Table<Refund>
              size="small"
              rowKey="operation_id"
              scroll={{ x: 760 }}
              dataSource={
                query.data?.items.filter((item) => item.order_id === orderId) ||
                []
              }
              columns={[
                {
                  title: "操作号",
                  render: (_, value) => (
                    <Button
                      type="link"
                      onClick={() => choose(value.operation_id)}
                    >
                      {value.operation_id}
                    </Button>
                  ),
                },
                {
                  title: "金额",
                  render: (_, value) =>
                    money(value.amount_minor, value.currency),
                },
                {
                  title: "权益处理",
                  dataIndex: "entitlement_action",
                  render: (value) => refundPolicyLabels[String(value)],
                },
                {
                  title: "状态",
                  dataIndex: "status",
                  render: (value) => <Status value={value} />,
                },
              ]}
            />
          </Card>
        </QueryPanel>
      ) : (
        <Unavailable
          title="此账户没有退款记录读取权限"
          description="查询操作结果需要 billing.ledger.read；发起申请和审批是独立权限。已收到的申请结果仍可核对其操作号。"
        />
      )}
      {selected && read && (
        <QueryPanel query={detail}>
          <Card title="退款详情">
            {current && (
              <>
                <Details
                  data={current}
                  fields={[
                    ["operation_id", "退款号"],
                    ["requested_by", "原申请人"],
                    ["version", "版本"],
                    [
                      "amount_minor",
                      "申请金额",
                      (value) => money(value, current.currency),
                    ],
                    ["status", "状态", (value) => <Status value={value} />],
                    [
                      "entitlement_action",
                      "权益处理",
                      (value) => refundPolicyLabels[String(value)],
                    ],
                  ]}
                />
                {refundReason(current.manual_reason) && (
                  <Alert
                    className="mb mt"
                    type="warning"
                    title={refundReason(current.manual_reason)}
                  />
                )}
                <Table
                  rowKey="id"
                  size="small"
                  className="mt"
                  scroll={{ x: 760 }}
                  dataSource={current.legs}
                  columns={[
                    {
                      title: "原收款来源",
                      dataIndex: "source_kind",
                      render: (value) =>
                        value === "balance" ? "原站内余额" : "原支付渠道",
                    },
                    { title: "来源标识", dataIndex: "source_id" },
                    {
                      title: "金额",
                      render: (_, value) =>
                        money(value.amount_minor, current.currency),
                    },
                    {
                      title: "财务进度",
                      render: (_, value) => (
                        <Space orientation="vertical">
                          <Status value={value.status} />
                          <Typography.Text>
                            {refundFinancialState(value)}
                          </Typography.Text>
                        </Space>
                      ),
                    },
                    { title: "渠道退款凭据", dataIndex: "provider_refund_id" },
                    { title: "账本交易号", dataIndex: "ledger_transaction_id" },
                    {
                      title: "凭据登记",
                      render: (_, leg) =>
                        leg.external_result ? (
                          <Space orientation="vertical">
                            <Typography.Text>渠道凭据已保存</Typography.Text>
                            <Typography.Text type="secondary">
                              {leg.external_result.recorded_at}
                            </Typography.Text>
                          </Space>
                        ) : canRecordRefundEvidence(current, leg) &&
                          can("billing.refund.execute") ? (
                          <Button
                            disabled={
                              Boolean(evidence) ||
                              unresolved.some(
                                (item) => item.saved.kind !== "execute",
                              )
                            }
                            onClick={() =>
                              setEvidence({ refund: current, legId: leg.id })
                            }
                          >
                            登记渠道已退款凭据
                          </Button>
                        ) : leg.source_kind === "balance" ? (
                          "由原站内账本处理"
                        ) : (
                          "—"
                        ),
                    },
                  ]}
                />
                {current.requested_by === subject &&
                  current.requires_second_reviewer !== false && (
                    <Alert
                      className="mt"
                      type="warning"
                      title={
                        current.requires_second_reviewer === true
                          ? "本申请需要另一位管理员批准"
                          : "双人审批策略尚未返回，申请人暂不能自行批准"
                      }
                      description="本人可填写理由撤回尚未执行的申请。最终审批约束由服务器校验。"
                    />
                  )}
                <Space wrap className="mt">
                  {can("billing.refund.approve") &&
                    current.status === "pending_review" && (
                      <Button
                        type="primary"
                        disabled={
                          !canApproveRefund(current, subject) ||
                          unresolved.length > 0
                        }
                        onClick={() => void review(current, "approve")}
                      >
                        批准申请
                      </Button>
                    )}
                  {can("billing.refund.approve") &&
                    ["pending_review", "approved"].includes(current.status) && (
                      <Button
                        danger
                        disabled={unresolved.length > 0}
                        onClick={() => void review(current, "reject")}
                      >
                        {current.requested_by === subject
                          ? "撤回申请"
                          : "驳回申请"}
                      </Button>
                    )}
                  {can("billing.refund.execute") && (
                    <Button
                      type="primary"
                      danger
                      disabled={
                        !canExecuteRefund(current) ||
                        unresolved.length > 0 ||
                        saved.some(
                          (item) =>
                            item.saved.operationId === current.operation_id &&
                            item.saved.kind === "execute" &&
                            item.saved.state !== "rejected",
                        )
                      }
                      onClick={() => void execute(current)}
                    >
                      提交退款执行
                    </Button>
                  )}
                </Space>
                <Typography.Paragraph className="mt" type="secondary">
                  {current.status === "approved"
                    ? "已批准，等待有执行权限的管理员提交。执行动作发送后只跟踪此退款号。"
                    : current.status === "pending_review"
                      ? "审批通过后才能执行退款。"
                      : refundResultMessage(current)}
                </Typography.Paragraph>
              </>
            )}
          </Card>
        </QueryPanel>
      )}
    </div>
  );
}

function RefundEvidenceForm({
  refund,
  legId,
  onClose,
  onSaved,
  onResult,
}: {
  refund: Refund;
  legId: string;
  onClose: () => void;
  onSaved: () => void;
  onResult: (value: Refund) => Promise<void>;
}) {
  const { api, principal } = useAuth();
  const subject = principalSubject(principal);
  const isActive = useActiveView(`${subject}:${refund.operation_id}:${legId}`);
  const draft = useDraft(
    subject,
    `refund-evidence:${refund.operation_id}:${legId}`,
  );
  const [form] = Form.useForm<Row>();
  const [busy, setBusy] = useState(false);
  const [hashing, setHashing] = useState(false);
  const [fileName, setFileName] = useState("");
  const [error, setError] = useState("");
  const lock = useRef(false);
  const hashGeneration = useRef(0);
  const command = useRef<RefundCommand | undefined>(undefined);
  const leg = refund.legs.find((value) => value.id === legId)!;
  let initial: Row = {};
  try {
    initial = JSON.parse(draft.value || "{}");
  } catch {
    /* Retain only valid local form drafts. */
  }
  const persistDraft = () =>
    draft.setValue(JSON.stringify(form.getFieldsValue()));
  const selectFile = async (file?: File) => {
    if (!file) return;
    const generation = ++hashGeneration.current;
    setHashing(true);
    setError("");
    form.setFieldValue("evidence_sha256", "");
    try {
      const hash = await hashRefundEvidence(file);
      if (isActive() && generation === hashGeneration.current) {
        form.setFieldValue("evidence_sha256", hash);
        setFileName(file.name);
        persistDraft();
      }
    } catch (value) {
      if (isActive()) setError(failure(value).message);
    } finally {
      if (isActive() && generation === hashGeneration.current)
        setHashing(false);
    }
  };
  const submit = async () => {
    if (lock.current || hashing) return;
    lock.current = true;
    setBusy(true);
    setError("");
    try {
      const values = await form.validateFields();
      if (values.confirmed !== true)
        throw new Error(
          "请确认已在原支付渠道核实退款完成，不能将申请中或口头承诺作为到账凭据",
        );
      command.current ||= RefundCommand.external(subject, refund, legId, {
        provider_refund_id: text(values.provider_refund_id, "").trim(),
        evidence_reference: text(values.evidence_reference, "").trim(),
        evidence_sha256: text(values.evidence_sha256, "").trim(),
      });
      onSaved();
      const result = await command.current.send(api);
      if (isActive()) {
        draft.clear();
        await onResult(result);
      }
    } catch (value) {
      if (isActive()) setError(failure(value).message);
    } finally {
      lock.current = false;
      if (isActive()) {
        setBusy(false);
        onSaved();
      }
    }
  };
  return (
    <Card
      title="登记原支付渠道已完成的退款"
      className="mb"
      extra={<Button onClick={onClose}>关闭并保留草稿</Button>}
    >
      <Alert
        className="mb"
        type="warning"
        showIcon
        title="此操作记录已经发生的渠道退款，并继续本地入账；不会向渠道再发一笔退款。"
        description={`原收款来源 ${leg.source_id} · 本项必须完整退回 ${money(leg.amount_minor, refund.currency)}。站内余额不能用渠道凭据代替账本处理。`}
      />
      {error && <Alert className="mb" type="error" showIcon title={error} />}
      {!draft.persisted && (
        <Alert
          className="mb"
          type="warning"
          title="草稿暂时无法保存，请勿关闭本页；资金动作发送前仍必须成功保存恢复信息。"
        />
      )}
      <Form
        form={form}
        layout="vertical"
        initialValues={initial}
        disabled={busy || Boolean(command.current)}
        onValuesChange={persistDraft}
      >
        <Form.Item
          name="provider_refund_id"
          label="渠道退款流水号"
          rules={[{ required: true, whitespace: true }, { max: 256 }]}
        >
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="evidence_reference"
          label="可复核的凭证存放位置或渠道记录"
          rules={[
            { required: true, whitespace: true },
            { min: 8, max: 2000 },
          ]}
          extra="填写团队可以访问的渠道记录号、归档位置或凭证地址。文件只在本机计算校验值，不会上传。"
        >
          <Input.TextArea rows={3} />
        </Form.Item>
        <Form.Item
          label="选取本机退款凭证（不上传）"
          extra={
            hashing
              ? "正在本机计算文件校验值…"
              : fileName
                ? `已计算：${fileName}。请自行将此原始文件保存在上方可复核位置。`
                : "选取不超过32MB的原始凭证文件；浏览器自动计算SHA256。"
          }
        >
          <input
            aria-label="本机退款凭证文件"
            type="file"
            disabled={busy || Boolean(command.current)}
            onChange={(event) =>
              void selectFile(event.currentTarget.files?.[0])
            }
          />
        </Form.Item>
        <Form.Item
          name="evidence_sha256"
          label="凭证校验值"
          rules={[
            { required: true },
            {
              pattern: /^[0-9a-f]{64}$/,
              message: "请选择文件生成校验值，或在高级输入填写有效的SHA256",
            },
          ]}
        >
          <Input readOnly placeholder="选取文件后自动生成" />
        </Form.Item>
        <Collapse
          className="mb"
          items={[
            {
              key: "advanced",
              label: "高级：已有SHA256校验值",
              children: (
                <>
                  <Typography.Paragraph type="secondary">
                    仅在已有原始凭证的可靠校验值时使用。校验值不能代替真实渠道到账证据。
                  </Typography.Paragraph>
                  <Input
                    aria-label="手填已有SHA256"
                    maxLength={64}
                    disabled={busy || hashing || Boolean(command.current)}
                    onChange={(event) => {
                      ++hashGeneration.current;
                      form.setFieldValue("evidence_sha256", event.target.value);
                      setFileName("");
                      persistDraft();
                    }}
                  />
                </>
              ),
            },
          ]}
        />
        <Form.Item
          name="confirmed"
          valuePropName="checked"
          rules={[
            {
              validator: async (_, value) => {
                if (value !== true)
                  throw new Error("请先核实原支付渠道已完成此项退款");
              },
            },
          ]}
        >
          <Checkbox>
            我已核实原支付渠道完成此项退款，金额和币种与上方完全一致。
          </Checkbox>
        </Form.Item>
        <Button
          type="primary"
          danger
          loading={busy}
          disabled={hashing || Boolean(command.current)}
          onClick={() => void submit()}
        >
          保存凭据并核对本地入账
        </Button>
      </Form>
      {command.current && (
        <Typography.Paragraph className="mt" type="secondary">
          原凭据和动作号已冻结。结果不明时，请使用上方恢复记录查询或重试原凭据的本地结算。
        </Typography.Paragraph>
      )}
    </Card>
  );
}

function RefundRequestForm({
  orderId,
  onClose,
  onSaved,
  onResult,
}: {
  orderId: string;
  onClose: () => void;
  onSaved: () => void;
  onResult: (refund: Refund) => Promise<void>;
}) {
  const { api, principal } = useAuth();
  const isActive = useActiveView(`${principalSubject(principal)}:${orderId}`);
  const [form] = Form.useForm<Row>();
  const [preview, setPreview] = useState<RefundPreview>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const lock = useRef(false);
  const command = useRef<RefundCommand | undefined>(undefined);
  const run = async (task: () => Promise<void>) => {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    setError("");
    try {
      await task();
    } catch (value) {
      if (isActive()) setError(failure(value).message);
    } finally {
      lock.current = false;
      if (isActive()) {
        setBusy(false);
        onSaved();
      }
    }
  };
  const inspect = () =>
    run(async () => {
      const values = await form.validateFields([
        "amount",
        "entitlement_action",
      ]);
      const amount = parseMinor(text(values.amount, ""), { zero: false });
      const result = await api.request(
        `v1/orders/${enc(orderId)}/refund-preview`,
        refundPreviewSchema.refine((value) => value.order_id === orderId),
        {
          method: "POST",
          body: {
            amount_minor: amount,
            entitlement_action: values.entitlement_action,
          },
        },
      );
      if (isActive()) {
        setPreview(result);
        form.setFieldValue("allocation", {});
      }
    });
  const submit = () =>
    run(async () => {
      if (!preview) throw new Error("请先读取当前可退款来源");
      const values = await form.validateFields();
      if (!command.current) {
        const allocations = validateRefundAllocation(
          preview,
          text(values.amount, ""),
          text(values.entitlement_action),
          (values.allocation as Record<string, unknown>) || {},
        );
        command.current = RefundCommand.request(
          principalSubject(principal),
          orderId,
          { ...allocations, reason: text(values.reason, "").trim() },
          preview.currency,
        );
        onSaved();
      }
      const result = await command.current.send(api);
      if (isActive()) await onResult(result);
    });
  return (
    <Card
      title="申请退款 · 先预览，再明确分配"
      className="mb"
      extra={
        <Button onClick={onClose}>
          {command.current ? "关闭，保留恢复记录" : "取消申请"}
        </Button>
      }
    >
      {error && <Alert type="error" showIcon title={error} className="mb" />}
      <Form
        form={form}
        layout="vertical"
        disabled={busy || Boolean(command.current)}
        initialValues={{ entitlement_action: "retain" }}
        onValuesChange={(changed) => {
          if ("amount" in changed || "entitlement_action" in changed)
            setPreview(undefined);
        }}
      >
        <Form.Item
          name="amount"
          label="申请金额（元）"
          rules={[{ required: true }]}
        >
          <Input inputMode="decimal" placeholder="例如 10.00" />
        </Form.Item>
        <Form.Item
          name="entitlement_action"
          label="退款后的订阅权益"
          rules={[{ required: true }]}
        >
          <Select
            options={entitlementActions.map((value) => ({
              value,
              label: refundPolicyLabels[value],
            }))}
          />
        </Form.Item>
        <Alert
          className="mb"
          type="info"
          title="保留权益：退款后仍保留订阅；撤销订阅：必须退还订单全部剩余已收资金，复杂权益来源需人工处理。"
        />
        <Button
          disabled={busy || Boolean(command.current)}
          onClick={() => void inspect()}
        >
          读取可退款来源
        </Button>
        {preview && (
          <div className="mt">
            <Details
              data={preview}
              fields={[
                [
                  "paid_amount_minor",
                  "订单已收",
                  (value) => money(value, preview.currency),
                ],
                [
                  "refunded_amount_minor",
                  "已退款",
                  (value) => money(value, preview.currency),
                ],
                [
                  "reserved_amount_minor",
                  "其他申请占用",
                  (value) => money(value, preview.currency),
                ],
                [
                  "available_amount_minor",
                  "当前可申请",
                  (value) => money(value, preview.currency),
                ],
                [
                  "requires_second_reviewer",
                  "审批要求",
                  (value) =>
                    value === true
                      ? "需另一位管理员批准"
                      : value === false
                        ? "按审批权限处理"
                        : "策略尚未返回",
                ],
              ]}
            />
            {refundReason(preview.manual_reason) && (
              <Alert
                className="mt mb"
                type="warning"
                title={refundReason(preview.manual_reason)}
                description="可以记录申请供后续核对；创建申请和审批不保证能自动退款。"
              />
            )}
            <Typography.Paragraph className="mt">
              请按原收款来源填写各项退回金额。预览不会自动分配；空白或0表示本次不从该来源退回。
            </Typography.Paragraph>
            {preview.sources.map((source) => (
              <Form.Item
                key={`${source.source_kind}:${source.source_id}`}
                name={[
                  "allocation",
                  `${source.source_kind}:${source.source_id}`,
                ]}
                label={`${source.source_kind === "balance" ? "原站内余额" : `原支付渠道 ${source.provider_code || ""}`} · 可退 ${money(source.available_amount_minor, preview.currency)}`}
                extra={`来源 ${source.source_id}`}
              >
                <Input inputMode="decimal" placeholder="本来源退款金额（元）" />
              </Form.Item>
            ))}
            <Form.Item
              name="reason"
              label="申请原因"
              rules={[
                { required: true },
                { min: 5, max: 2000, message: "请输入5至2000个字符" },
              ]}
            >
              <Input.TextArea rows={3} />
            </Form.Item>
            <Button
              type="primary"
              loading={busy}
              disabled={Boolean(command.current)}
              onClick={() => void submit()}
            >
              冻结参数并提交申请
            </Button>
          </div>
        )}
      </Form>
    </Card>
  );
}
