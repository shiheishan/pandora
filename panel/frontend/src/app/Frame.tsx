/**
 * [INPUT]: 依赖 core/auth 的 useAuth、core/runtime 的 runtime 与 legacyEntry、core/realtime 的 useRealtime，依赖 ./navigation、./sections、./SidebarMenu、./Login，依赖 antd 布局组件与 react-router 的 Outlet
 * [OUTPUT]: 对外提供 Frame 组件
 * [POS]: app 壳层的已登录布局：侧栏导航、面包屑、账户菜单（返回原版、退出），业务页经 Outlet 渲染在其中；未登录时退回 Login
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from "react";
import {
  Avatar,
  Breadcrumb,
  Button,
  Drawer,
  Dropdown,
  Layout,
  Result,
  Skeleton,
  Space,
  Tag,
  Typography,
} from "antd";
import { LogoutOutlined, MenuOutlined, MenuFoldOutlined, MenuUnfoldOutlined, MoreOutlined } from "@ant-design/icons";
import { Link, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useAuth } from "../core/auth";
import { legacyEntry, runtime } from "../core/runtime";
import { useRealtime } from "../core/realtime";
import { adminNav, portalNav } from "./navigation";
import { visibleSections, pageNavigation, systemConfigPaths } from "./sections";
import { SidebarMenu } from "./SidebarMenu";
import { SettingsLayout } from "./SettingsLayout";
import { Login } from "./Login";
import { useBranding, PortalSlot } from "../core/appearance";

export function Frame() {
  const branding = useBranding();
  const { principal, ready, can, logout } = useAuth();
  const [drawer, setDrawer] = useState(false);
  const [collapsed, setCollapsed] = useState(false);
  const location = useLocation();
  const navigate = useNavigate();
  const connection = useRealtime();
  if (!ready)
    return (
      <div className="initial-loading">
        <Skeleton active />
      </div>
    );
  if (!principal) return <Login />;
  const navigation = (runtime.domain === "admin" ? adminNav : portalNav).filter(
    (item) => can(item.permission),
  );
  const active = location.pathname.split("/")[1] || "overview";
  const current = navigation.find((item) => item.path === active);
  const sections = visibleSections(navigation);
  const section = sections.find(item => item.paths.includes(active));
  const relatedPages = runtime.domain === "admin" ? pageNavigation(navigation, active) : [];
  const menu = (compact = false) => (
    <>
      <Link className="brand" to="/overview">
        <span className="brand-mark">P</span>
        <div>
          <b>{branding.name}</b>
          <small>
            {runtime.domain === "admin" ? "管理控制台" : "用户中心"}
          </small>
        </div>
      </Link>
      <SidebarMenu
        navigation={navigation}
        active={active}
        compact={compact}
        onNavigate={path => {
          navigate("/" + path);
          setDrawer(false);
        }}
      />
      <PortalSlot name="portal.sidebar.extra" />
      <div className="sidebar-footer">
        <span className="status-dot" />
        {connection === "connected"
          ? "实时更新已连接"
          : connection === "reconnecting"
            ? "实时更新正在重连"
            : "按需刷新数据"}
      </div>
    </>
  );
  const known = (runtime.domain === "admin" ? adminNav : portalNav).find(
    (item) => item.path === active,
  );
  return (
    <Layout className={`app-layout${collapsed ? " sidebar-collapsed" : ""}`}>
      <Layout.Sider width={238} collapsedWidth={64} collapsed={collapsed} trigger={null} theme="light" className="desktop-sidebar">
        {menu(collapsed)}
      </Layout.Sider>
      <Drawer
        open={drawer}
        onClose={() => setDrawer(false)}
        placement="left"
        width={270}
        styles={{ body: { padding: 0 } }}
        title="导航"
      >
        {menu()}
      </Drawer>
      <Layout className="main-layout">
        <Layout.Header className="topbar">
          <Space>
            <Button
              className="mobile-menu"
              icon={<MenuOutlined />}
              aria-label="打开导航"
              onClick={() => setDrawer(true)}
            />
            <Button
              className="desktop-collapse"
              type="text"
              icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
              aria-label={collapsed ? "展开侧栏" : "收起侧栏"}
              onClick={() => setCollapsed(!collapsed)}
            />
            <Breadcrumb
              items={[
                { title: runtime.domain === "admin" ? "工作空间" : "用户中心" },
                { title: current?.title || "详情" },
              ]}
            />
          </Space>
          <Space>
            <Tag className="candidate-tag">新版预览</Tag>
            <Typography.Text className="account-email" type="secondary">
              {principal.email}
            </Typography.Text>
            <Dropdown
              menu={{
                items: [
                  { key: "legacy", label: "返回原版界面" },
                  {
                    key: "logout",
                    label: "退出登录",
                    icon: <LogoutOutlined />,
                    danger: true,
                  },
                ],
                onClick: ({ key }) =>
                  key === "logout"
                    ? void logout()
                    : window.location.assign(legacyEntry),
              }}
            >
              <Button
                className="account-menu"
                icon={<MoreOutlined />}
                aria-label="账户菜单"
              >
                <Avatar
                  size="small"
                  style={{ backgroundColor: "#dce6ff", color: "#2d50cb" }}
                >
                  {principal.email[0]?.toUpperCase()}
                </Avatar>
              </Button>
            </Dropdown>
          </Space>
        </Layout.Header>
        <Layout.Content className="content-area">
          {relatedPages.length > 1 && <nav className="workspace-pages" aria-label="相关管理页面">
            {relatedPages.map(item => <Link key={item.path} to={"/" + item.path}
              aria-current={item.path === active ? "page" : undefined}>
              {item.title}
            </Link>)}
          </nav>}
          {active === "overview" && <PortalSlot name="portal.home.banner" />}
          {active === "subscriptions" && <PortalSlot name="portal.subscribe.notice" />}
          {active === "plans" && location.pathname === "/plans" && <PortalSlot name="portal.plans.notice" />}
          {known && !can(known.permission) ? (
            <Result
              status="403"
              title="没有访问权限"
              subTitle="当前账户无法查看此页面，请联系管理员。"
            />
          ) : runtime.domain === "admin" && section && systemConfigPaths.includes(active) ? (
            <SettingsLayout items={section.items} active={active}><Outlet key={location.pathname} /></SettingsLayout>
          ) : (
            <Outlet key={location.pathname} />
          )}
        {active === "overview" && <PortalSlot name="portal.home.aside" />}
        </Layout.Content>
        <Layout.Footer className="footer">
          <PortalSlot name="portal.footer" />
          Pandora ·{" "}
          {runtime.domain === "admin"
            ? "让每项运营工作都有清楚的结果"
            : "简单连接，安心使用"}
        </Layout.Footer>
      </Layout>
    </Layout>
  );
}
