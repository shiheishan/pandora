import { z } from "zod";

export type Row = Record<string, unknown>;
export const recordSchema = z.record(z.string(), z.unknown());
export const createdIdSchema = z.object({ id: z.uuid() }).passthrough();
export const createdOrderSchema = z
  .object({ order_id: z.uuid() })
  .passthrough();
export const createdResourceSchema = (key: string) =>
  z.object({ [key]: createdIdSchema }).passthrough();
export const generatedUsersSchema = z
  .object({
    count: z.number().int().positive(),
    users: z
      .array(z.object({ email: z.email(), password: z.string().min(1) }))
      .min(1),
  })
  .passthrough()
  .refine(
    (value) => value.count === value.users.length,
    "生成账户数量与结果不一致",
  );
export const loginSchema = z
  .object({ access_token: z.string().min(1) })
  .passthrough();
export const principalSchema = z
  .object({
    id: z.string().optional(),
    user_id: z.string().optional(),
    email: z.string().optional(),
    permissions: z.array(z.string()).optional(),
  })
  .passthrough()
  .refine((value) => Boolean(value.id || value.user_id), "缺少账户身份")
  .transform((value) => ({
    ...value,
    email: value.email || value.user_id || value.id || "管理员",
  }));
export type Principal = z.infer<typeof principalSchema>;
export function principalSubject(
  principal: Principal | null | undefined,
): string {
  return principal?.user_id || principal?.id || principal?.email || "";
}

export function isRow(value: unknown): value is Row {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
export function record(value: unknown): Row {
  return isRow(value) ? value : {};
}
export function text(value: unknown, fallback = "—"): string {
  return typeof value === "string" && value !== ""
    ? value
    : typeof value === "number" || typeof value === "bigint"
      ? String(value)
      : fallback;
}
export function list(value: unknown): Row[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value) || !value.every(isRow))
    throw new Error("列表响应格式不符合接口约定");
  return value;
}
export function rows(data: Row | undefined, key: string): Row[] {
  return list(data?.[key]);
}
export function integer(value: unknown, fallback = 0): number {
  if (typeof value === "number" && Number.isSafeInteger(value)) return value;
  if (typeof value === "string" && /^-?\d+$/.test(value)) {
    const parsed = Number(value);
    if (Number.isSafeInteger(parsed)) return parsed;
  }
  return fallback;
}
export function idOf(row: Row): string {
  return text(row.id ?? row.code ?? row.key, "");
}
export function enc(value: unknown): string {
  return encodeURIComponent(text(value, ""));
}
export function dateText(value: unknown): string {
  if (typeof value !== "string" || !value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.valueOf())
    ? "时间格式异常"
    : date.toLocaleString("zh-CN", { hour12: false });
}
export function queryString(
  values: Record<string, string | number | undefined>,
): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values))
    if (value !== undefined && value !== "") params.set(key, String(value));
  return params.toString();
}
