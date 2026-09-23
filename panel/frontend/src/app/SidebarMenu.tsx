import { useEffect, useState } from "react";
import { Menu } from "antd";
import { runtime } from "../core/runtime";
import type { NavItem } from "./navigation";
import { groupedSidebar } from "./sections";

export function SidebarMenu({ navigation, active, compact, onNavigate }: {
  navigation: NavItem[];
  active: string;
  compact: boolean;
  onNavigate: (path: string) => void;
}) {
  const admin = runtime.domain === "admin";
  const sidebar = groupedSidebar(navigation, active);
  const groups = [...new Set(navigation.map(item => item.group))];
  const available = admin ? sidebar.openKeys : groups;
  const activeGroup = admin ? sidebar.activeGroup : navigation.find(item => item.path === active)?.group;
  const [openKeys, setOpenKeys] = useState(available);
  const availableKey = JSON.stringify(available);
  useEffect(() => {
    const allowed = JSON.parse(availableKey) as string[];
    setOpenKeys(previous => {
      const next = previous.filter(key => allowed.includes(key));
      if (activeGroup && !next.includes(activeGroup)) next.push(activeGroup);
      return next;
    });
  }, [active, activeGroup, availableKey]);
  return <Menu
    mode="inline"
    inlineCollapsed={compact}
    openKeys={compact ? [] : openKeys}
    onOpenChange={keys => { if (!compact) setOpenKeys(keys); }}
    selectedKeys={[admin ? sidebar.selected : active]}
    items={admin ? sidebar.items : groups.map(group => ({
      key: group,
      icon: navigation.find(item => item.group === group)?.icon,
      label: group,
      children: navigation.filter(item => item.group === group).map(item => ({
        key: item.path, label: item.title, icon: item.icon,
      })),
    }))}
    onClick={({ key }) => onNavigate(key)}
  />;
}
