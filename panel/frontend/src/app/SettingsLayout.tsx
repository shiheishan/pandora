import { Menu } from "antd";
import { useNavigate } from "react-router-dom";
import type { ReactNode } from "react";
import type { NavItem } from "./navigation";
import { settingsGroups } from "./sections";
const labels: Record<string, string> = {
  telegram: "Telegram 设置",
  notifications: "邮件与通知", "payment-providers": "支付配置",
  appearance: "主题与外观", plugins: "插件管理", announcements: "公告管理",
  content: "知识库管理", risk: "安全与访问", switches: "服务开关", audit: "审计日志",
};
export function SettingsLayout({ items, active, children }: { items: NavItem[]; active: string; children: ReactNode }) {
  const navigate = useNavigate();
  const groups = settingsGroups.map(group => ({ key: group.title, type: "group" as const,
    label: group.title, children: group.paths.flatMap(path => items.filter(item => item.path === path)
      .map(item => ({ key: item.path, icon: item.icon, label: labels[item.path] || item.title }))),
  })).filter(group => group.children.length > 0);
  return <>
    <div className="settings-heading"><h1>系统配置</h1><p>管理通知、安全与运行配置。</p></div>
    <div className="settings-workspace">
      <nav aria-label="设置分类" className="settings-navigation">
        <Menu mode="inline" selectedKeys={[active]} items={groups} onClick={({ key }) => navigate("/" + key)} />
      </nav>
      <section className="settings-detail">{children}</section>
    </div>
  </>;
}
