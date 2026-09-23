import { expect, it } from "vitest";
import { protocolChanges, protocolChoices } from "../src/features/admin/ProtocolEditor";
const schema = { sensitive_properties: ["private_key"], property_types: { "network_settings.path": "string", headers: "object", tls: "number" } };
it("sends only edited fields and never sends a blank hidden key", () => {
  const initial = { "network_settings.path": "/old", "reality_settings.private_key": "", tls: 2 };
  expect(protocolChanges(schema, initial, { ...initial, "network_settings.path": "/new" }, {})).toEqual({ network_settings: { path: "/new" } });
});
it("supports explicit replacement secrets and removal of optional fields", () => {
  const initial = { "reality_settings.private_key": "", "network_settings.path": "/old" };
  expect(protocolChanges(schema, initial, { "reality_settings.private_key": "replacement-fixture", "network_settings.path": "" }, {})).toEqual({ reality_settings: { private_key: "replacement-fixture" }, network_settings: { path: null } });
});
it("preserves literal legacy dotted fields and rejects malformed structured parameters", () => {
  expect(protocolChanges(schema, { "network_settings.path": "/old" }, { "network_settings.path": "/new" }, { "network_settings.path": "/old" })).toEqual({ "network_settings.path": "/new" });
  expect(() => protocolChanges(schema, { headers: "{}" }, { headers: "invalid" }, {})).toThrow();
});

it("does not offer TLS modes omitted by the server protocol schema", () => {
  expect(protocolChoices({ property_types: { tls: "number" }, enums: { tls: ["0", "2"] } }, "tls")).toEqual([{ value: 0, label: "关闭 TLS" }, { value: 2, label: "REALITY" }]);
  expect(protocolChoices({ property_types: { tls: "number" }, enums: { tls: ["1", "2"] } }, "tls").map(option => option.value)).toEqual([1, 2]);
});
