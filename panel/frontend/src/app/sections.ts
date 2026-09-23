import type { NavItem } from "./navigation";
export const adminSections = [
  { title: "概览", paths: ["overview"] },
  { title: "用户", paths: ["users", "user-groups", "device-limits", "bulk", "traffic-resets"] },
  { title: "工单", paths: ["tickets"] },
  { title: "节点与服务器", paths: ["nodes", "servers", "node-pools", "routing"] },
  { title: "套餐", paths: ["plans"] },
  { title: "订单与收款", paths: ["orders", "late-payments"] },
  { title: "营销", paths: ["coupons", "gift-cards", "commissions"] },
  { title: "设置", paths: ["commission-settings", "notifications", "telegram", "payment-providers", "appearance", "plugins", "announcements", "content", "risk", "switches", "audit"] },
];
export const settingsGroups = [
  { title: "订阅与佣金", paths: ["commission-settings"] },
  { title: "通知配置", paths: ["notifications", "telegram"] },
  { title: "安全与运维", paths: ["risk", "switches", "audit"] },
];
// Only configuration pages share the inner navigation. Standalone management
// pages already have direct sidebar entries and must keep the full content width.
export const systemConfigPaths = settingsGroups.flatMap(group => group.paths);
export function visibleSections(navigation: NavItem[]) {
  return adminSections.map(section => ({ ...section,
    items: section.paths.flatMap(path => navigation.filter(item => item.path === path)),
  })).filter(section => section.items.length > 0);
}
export const sidebarGroups = [
  { title: "系统管理", entries: [
    { title: "系统配置", paths: ["notifications", "telegram", "commission-settings", "risk", "switches", "audit"] },
    { title: "插件管理", paths: ["plugins"] },
    { title: "主题配置", paths: ["appearance"] },
    { title: "公告管理", paths: ["announcements"] },
    { title: "支付配置", paths: ["payment-providers"] },
    { title: "知识库", paths: ["content"] },
  ] },
  { title: "节点管理", entries: [
    { title: "服务器管理", paths: ["servers"] },
    { title: "节点管理", paths: ["nodes"] },
    { title: "权限组", paths: ["node-pools"] },
    { title: "路由管理", paths: ["routing"] },
  ] },
  { title: "订阅管理", entries: [
    { title: "套餐", paths: ["plans"] },
    { title: "订单", paths: ["orders", "late-payments"] },
    { title: "优惠券", paths: ["coupons"] },
    { title: "礼品卡", paths: ["gift-cards"] },
  ] },
  { title: "用户管理", entries: [
    { title: "用户", paths: ["users", "user-groups", "device-limits", "bulk"] },
    { title: "工单", paths: ["tickets"] },
  ] },
  { title: "更多工具", entries: [
    { title: "流量重置", paths: ["traffic-resets"] },
    { title: "分销管理", paths: ["commissions"] },
  ] },
];
export function groupedSidebar(navigation: NavItem[], active: string) {
  let selected = active;
  const overview = navigation.find(item => item.path === "overview");
  const groups = sidebarGroups.map(group => {
    const children = group.entries.flatMap(entry => {
      const item = entry.paths.flatMap(path => navigation.filter(item => item.path === path))[0];
      if (!item) return [];
      if (entry.paths.includes(active)) selected = item.path;
      return [{ key: item.path, label: entry.title, icon: item.icon }];
    });
    return { key: `group:${group.title}`, label: group.title, icon: children[0]?.icon, children };
  }).filter(group => group.children.length);
  return { selected, openKeys: groups.map(group => group.key),
    activeGroup: groups.find(group => group.children.some(item => item.key === selected))?.key,
    items: [...(overview ? [{ key: overview.path, label: "仪表盘", icon: overview.icon }] : []), ...groups] };
}

// A compact sidebar entry can cover several pages, but each page still needs
// a visible destination. System settings already have their own navigation.
export function pageNavigation(navigation: NavItem[], active: string) {
  if (systemConfigPaths.includes(active)) return [];
  const entry = sidebarGroups.flatMap(group => group.entries).find(item =>
    item.paths.length > 1 && item.paths.includes(active));
  return entry ? entry.paths.flatMap(path => navigation.filter(item => item.path === path)) : [];
}
