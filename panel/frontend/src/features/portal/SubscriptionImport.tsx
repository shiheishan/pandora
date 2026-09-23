import { useRef, useState } from "react";
import { Alert, App, Button, Input, QRCode, Space, Typography } from "antd";
import { useBranding } from "../../core/appearance";

const b64url = (value: string) => btoa(Array.from(new TextEncoder().encode(value), byte => String.fromCharCode(byte)).join("")).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
export function importLinks(raw: string, name: string): { name: string; href: string; unavailable?: string }[] {
  const url = new URL(raw);
  if (!["https:", "http:"].includes(url.protocol) || url.username || url.password) throw new Error("订阅地址格式异常，请重新读取订阅链接");
  const target = (format: string) => {
    const source = new URL(raw);
    source.searchParams.set("target", format);
    return source.href;
  };
  return [
    { name: "Clash Verge / mihomo", href: `clash://install-config?url=${encodeURIComponent(target("clash"))}&name=${encodeURIComponent(name)}` },
    { name: "Stash", href: `stash://install-config?url=${encodeURIComponent(target("clash"))}` },
    { name: "Shadowrocket", href: `sub://${b64url(target("uri"))}` },
    { name: "sing-box", href: `sing-box://import-remote-profile?url=${encodeURIComponent(target("singbox"))}` },
    { name: "Surge", href: `surge:///install-config?url=${encodeURIComponent(target("surge"))}` },
    { name: "Loon", href: `loon://import?nodelist=${encodeURIComponent(target("loon"))}` },
    { name: "Quantumult X", href: `quantumult-x:///add-resource?remote-resource=${encodeURIComponent(JSON.stringify({ server_remote: [`${target("quantumult-x").replace(/,/g, "%2C")}, tag=${name.replace(/[,\r\n]/g, " ")}`] }))}` },
  ];
}

export function SubscriptionImport({ url }: { url: string }) {
  const { name } = useBranding();
  const { message } = App.useApp();
  const input = useRef<import("antd").InputRef>(null);
  const [hint, setHint] = useState("");
  let clients: ReturnType<typeof importLinks>;
  try { clients = importLinks(url, name); } catch { return <Alert type="error" showIcon title="订阅链接无效，请关闭后刷新订阅页面重试" />; }
  const copy = async () => {
    try { await navigator.clipboard.writeText(url); message.success("订阅链接已复制"); }
    catch { input.current?.select(); setHint("浏览器未允许自动复制，已选中链接，请手动复制。"); }
  };
  return <Space orientation="vertical" size="middle" style={{ width: "100%" }}>
    <Typography.Text type="secondary">选择已安装的客户端尝试导入。若没有唤起，请复制链接，在客户端中选择从 URL 导入。</Typography.Text>
    <Space wrap>{clients.map(client => <Button key={client.name} href={client.unavailable ? undefined : client.href} onClick={() => setHint(client.unavailable || `已请求打开 ${client.name}。请在客户端确认导入；面板无法确认导入是否成功。`)}>{client.name}{client.unavailable ? "（待适配）" : ""}</Button>)}</Space>
    <Button onClick={() => void copy()}>v2rayN / NekoBox 等：复制链接</Button>
    {new TextEncoder().encode(url).length <= 1200 ? <div style={{ display: "grid", justifyItems: "center", gap: 8 }}><QRCode value={url} type="svg" size={176} /><Typography.Text type="secondary">使用手机客户端扫描订阅二维码</Typography.Text></div> : <Typography.Text type="secondary">链接较长，请使用下方复制方式导入。</Typography.Text>}
    <Input ref={input} aria-label="订阅链接" value={url} readOnly onFocus={event => event.currentTarget.select()} />
    <Button onClick={() => void copy()}>复制订阅链接</Button>
    {hint && <Alert type="info" title={hint} />}
    <Typography.Text type="secondary">订阅链接及二维码包含访问凭据。Surge 导入独立配置，Loon 和 Quantumult X 添加节点订阅。各客户端只会收到可转换的节点；若节点较少或为空，可尝试 Clash / sing-box。AnyTLS 等协议需要较新客户端版本。</Typography.Text>
  </Space>;
}
