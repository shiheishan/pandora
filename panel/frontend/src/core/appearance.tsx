import { createContext, useContext, useEffect, type ReactNode } from "react";
import { ConfigProvider } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "./auth";
import { record, recordSchema, text } from "./data";
import { runtime } from "./runtime";

const defaults = { name: "PANDORA", tagline: "", slots: {} as Record<string, unknown> };
const AppearanceContext = createContext(defaults);
export const useBranding = () => useContext(AppearanceContext);
export function PortalSlot({ name }: { name: string }) {
  const { slots } = useBranding();
  const content = slots[name];
  if (runtime.domain !== "portal" || typeof content !== "string" || !content.trim()) return null;
  // Only persisted HTML returned by the existing sanitizing API is rendered here.
  return <div className="portal-slot" data-portal-slot={name} dangerouslySetInnerHTML={{ __html: content }} />;
}
export function PortalAppearance({ children }: { children: ReactNode }) {
  const { api } = useAuth();
  const portal = runtime.domain === "portal";
  const query = useQuery({ queryKey: ["public-appearance", runtime.apiBase],
    queryFn: ({ signal }) => api.request("v1/appearance", recordSchema, { signal, anonymous: true }),
    enabled: portal, staleTime: 60_000, gcTime: 10 * 60_000, retry: false,
    refetchInterval: 60_000, refetchIntervalInBackground: false, refetchOnWindowFocus: true,
  });
  const theme = record(query.data?.theme), branding = record(theme.branding), tokens = record(theme.tokens);
  const name = portal ? text(branding.site_name, "").trim() || defaults.name : defaults.name;
  const tagline = portal ? text(branding.tagline, "") : "";
  const color = (key: string) => typeof tokens[key] === "string" && globalThis.CSS?.supports?.("color", String(tokens[key])) ? String(tokens[key]) : undefined;
  const primary = portal ? color("brand") : undefined;
  useEffect(() => {
    if (!portal || !query.data) return;
    const originalTitle = document.title;
    document.title = name;
    const style = document.createElement("style");
    style.dataset.pandoraTheme = String(theme.code || "default");
    // Existing backend sanitizes CSS; textContent never interprets it as HTML.
    style.textContent = ".ant-card{border-radius:var(--r-lg,10px)}\n" + text(theme.custom_css, "");document.head.appendChild(style);
    const changed: [string, string][] = [];
    for (const key of ["brand", "brand-2", "brand-soft", "brand-on-soft", "r-lg"]) {
      const value = tokens[key];
      if (typeof value !== "string" || !globalThis.CSS?.supports?.(key === "r-lg" ? "border-radius" : "color", value)) continue;
      const property = `--${key}`;changed.push([property, document.documentElement.style.getPropertyValue(property)]);
      document.documentElement.style.setProperty(property, value);
    }
    return () => { style.remove(); document.title = originalTitle; for (const [key, value] of changed) { if (value) document.documentElement.style.setProperty(key, value); else document.documentElement.style.removeProperty(key); } };
  }, [portal, query.data, name]);
  if (!portal) return children;
  return <AppearanceContext.Provider value={{ name, tagline, slots: record(query.data?.slots) }}><ConfigProvider theme={{ token: { ...(primary ? { colorPrimary: primary } : {}) }, components: { Menu: { ...(color("brand-on-soft") || primary ? { itemSelectedColor: color("brand-on-soft") || primary } : {}), ...(color("brand-soft") ? { itemSelectedBg: color("brand-soft") } : {}) } } }}>{children}</ConfigProvider></AppearanceContext.Provider>;
}
