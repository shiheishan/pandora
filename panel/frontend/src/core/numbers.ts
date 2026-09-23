export function parseMinor(
  value: string,
  { signed = false, zero = true } = {},
): string {
  const cleaned = value.trim();
  const pattern = signed ? /^-?\d+(?:\.\d{1,2})?$/ : /^\d+(?:\.\d{1,2})?$/;
  if (!pattern.test(cleaned)) throw new Error("请输入有效金额，最多两位小数");
  const negative = cleaned.startsWith("-");
  const [whole = "0", fraction = ""] = cleaned.replace(/^-/, "").split(".");
  let amount = BigInt(whole) * 100n + BigInt(fraction.padEnd(2, "0"));
  if (negative) amount = -amount;
  if (!zero && amount === 0n) throw new Error("金额不能为零");
  if (amount < -9223372036854775808n || amount > 9223372036854775807n)
    throw new Error("金额超出允许范围");
  return amount.toString();
}
export function legacyInteger(value: string): number {
  const number = Number(value);
  if (
    !Number.isSafeInteger(number) ||
    BigInt(number).toString() !== BigInt(value).toString()
  )
    throw new Error("当前接口不支持这个数值范围，请联系管理员");
  return number;
}
export function asBigInt(value: unknown): bigint | null {
  if (typeof value === "bigint") return value;
  if (typeof value === "string" && /^-?\d+$/.test(value)) return BigInt(value);
  if (typeof value === "number" && Number.isSafeInteger(value))
    return BigInt(value);
  return null;
}
export function money(value: unknown, currency: unknown = "CNY"): string {
  const amount = asBigInt(value);
  if (amount === null) return "—";
  const abs = amount < 0n ? -amount : amount;
  const whole = (abs / 100n).toLocaleString("zh-CN");
  return `${typeof currency === "string" ? currency : "CNY"} ${amount < 0n ? "-" : ""}${whole}.${(abs % 100n).toString().padStart(2, "0")}`;
}
export function bytes(value: unknown): string {
  const amount = asBigInt(value);
  if (amount === null || amount < 0n) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let divisor = 1n;
  let index = 0;
  while (amount >= divisor * 1024n && index < units.length - 1) {
    divisor *= 1024n;
    index++;
  }
  const tenths = (amount * 10n) / divisor;
  return `${tenths / 10n}${index ? "." + (tenths % 10n) : ""} ${units[index]}`;
}
