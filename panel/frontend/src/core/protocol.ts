import { record, text, type Row } from "./data";
const strings = (value: unknown): string[] =>
  Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
export function protocolField(schema: Row, key: string) {
  const kind = text(record(schema.property_types)[key], "string");
  const leaf = key.split(".").at(-1) || key;
  const declared = strings(record(schema.enums)[key] ?? record(schema.enums)[leaf]);
  const methods =
    key === "cipher" || key === "method" ? strings(schema.methods) : [];
  const enums = methods.length ? methods : declared;
  return {
    kind,
    enums,
    sensitive: strings(schema.sensitive_properties).some(
      (item) => item === key || item === leaf,
    ),
    required: strings(schema.required).includes(key),
  };
}
