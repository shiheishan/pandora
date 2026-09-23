import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App } from "antd";
import { expect, it, vi } from "vitest";
import { importLinks, SubscriptionImport } from "../src/features/portal/SubscriptionImport";

it("pins each client format without losing credentials or replacing QX configuration", () => {
  const url = "https://example.test/sub/secret?target=clash&target=uri&token=a%2Bb%3Dc&name=中文#part";
  const links = importLinks(url, "测试站,\nextra=bad");
  expect(links).toHaveLength(7);
  const shadow = links[2]!.href.slice(6).replace(/-/g, "+").replace(/_/g, "/");
  const decodedShadow = new TextDecoder().decode(Uint8Array.from(atob(shadow), c => c.charCodeAt(0)));
  const remote = JSON.parse(new URL(links[6]!.href).searchParams.get("remote-resource")!).server_remote;
  expect(links[6]!.href).toContain("///add-resource?");
  expect(remote).toHaveLength(1);
  const sources = links.map((link,index) => index===2 ? decodedShadow : index===6 ? remote[0].split(", tag=")[0] : new URL(link.href).searchParams.get(index===5?"nodelist":"url"));
  sources.forEach((source,index) => {
    const parsed = new URL(source!);
    expect(parsed.searchParams.getAll("target")).toEqual([["clash","clash","uri","singbox","surge","loon","quantumult-x"][index]]);
    expect(parsed.searchParams.get("token")).toBe("a+b=c");
    expect(parsed.searchParams.get("name")).toBe("中文");
    expect(parsed.hash).toBe("#part");
    expect(parsed.pathname).toBe("/sub/secret");
  });
  expect(links[4]!.unavailable).toBeUndefined();
  expect(remote[0]).not.toContain("\n");
  expect(() => importLinks("javascript:alert(1)", "test")).toThrow();
  expect(() => importLinks("https://user:password@example.test/", "test")).toThrow();
});
it("copies the exact link and falls back to selected text if clipboard permission fails", async () => {
  const writeText = vi.fn().mockRejectedValue(new Error("denied"));
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  const url = "https://example.test/sub/private";
  render(<App><SubscriptionImport url={url} /></App>);
  expect(screen.getByRole("link", { name: "Clash Verge / mihomo" })).toHaveAttribute("href", importLinks(url, "PANDORA")[0]!.href);
  fireEvent.click(screen.getByRole("button", { name: "复制订阅链接" }));
  await screen.findByText(/浏览器未允许自动复制/);
  const input = screen.getByLabelText("订阅链接") as HTMLInputElement;
  expect(input.selectionStart).toBe(0);expect(input.selectionEnd).toBe(url.length);
  writeText.mockResolvedValue(undefined);
  fireEvent.click(screen.getByRole("button", { name: "复制订阅链接" }));
  await waitFor(() => expect(writeText).toHaveBeenLastCalledWith(url));
});
it("omits QR output for long UTF-8 links while keeping manual copy available", () => {
  render(<App><SubscriptionImport url={"https://example.test/" + "中".repeat(500)} /></App>);
  expect(screen.getByText(/链接较长/)).toBeVisible();
  expect(screen.getByRole("button", { name: "复制订阅链接" })).toBeEnabled();
});
