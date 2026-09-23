import React, {
  lazy,
  Suspense,
  type ComponentType,
  type ReactNode,
} from "react";
import ReactDOM from "react-dom/client";
import { App, Button, ConfigProvider, Result, Skeleton } from "antd";
import zhCN from "antd/locale/zh_CN";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createHashRouter,
  Navigate,
  RouterProvider,
  useRouteError,
} from "react-router-dom";
import { AuthProvider, useAuth } from "./core/auth";
import { DialogHost } from "./core/dialogs";
import { runtime } from "./core/runtime";
import { Gateway } from "./app/Gateway";
import { PortalAppearance } from "./core/appearance";

import "./styles.css";

function lazyPage(load: () => Promise<{ default: ComponentType }>) {
  const Page = lazy(load);
  return () => (
    <Suspense fallback={<Skeleton active paragraph={{ rows: 8 }} />}>
      <Page />
    </Suspense>
  );
}
const GiftCardsPage = lazy(() => import("./features/admin/GiftCards").then(module => ({ default: module.GiftCardsPage })));
const PluginsPage = lazy(() => import("./features/admin/Plugins").then(module => ({ default: module.PluginsPage })));
const CouponsPage = lazy(() => import("./features/admin/Coupons").then(module => ({ default: module.CouponsPage })));
const LazyOperations = lazy(() => import("./features/admin/Operations").then(module => ({ default: module.OperationsPage })));
const OperationsPage = ({ screen }: { screen: string }) => (
  <Suspense fallback={<Skeleton active paragraph={{ rows: 8 }} />}>
    <LazyOperations screen={screen} />
  </Suspense>
);
const NotificationSettings = lazyPage(() => import("./features/admin/Operations").then(module => ({ default: module.NotificationSettings })));
const TelegramSettings = lazyPage(() => import("./features/admin/TelegramSettings").then(module => ({ default: module.TelegramSettings })));
const AppearancePage = lazyPage(() => import("./features/admin/Appearance").then(module => ({ default: module.AppearancePage })));
const CommissionSettings = lazyPage(() => import("./features/admin/CommissionSettings").then(module => ({ default: module.CommissionSettings })));
const UserGroupsPage = lazyPage(() => import("./features/admin/UserGroups").then(module => ({ default: module.UserGroupsPage })));
const RoutingPage = lazyPage(() => import("./features/admin/Routing").then(module => ({ default: module.RoutingPage })));
const BulkPage = lazyPage(() => import("./features/admin/Operations").then(module => ({ default: module.BulkPage })));
const Overview = lazyPage(() =>
  import("./features/Overview").then((module) => ({
    default: module.Overview,
  })),
);
const UsersPage = lazyPage(() =>
  import("./features/admin/Users").then((module) => ({
    default: module.UsersPage,
  })),
);
const UserDetail = lazyPage(() =>
  import("./features/admin/Users").then((module) => ({
    default: module.UserDetail,
  })),
);
const ServersPage = lazyPage(() =>
  import("./features/admin/Nodes").then((module) => ({
    default: module.ServersPage,
  })),
);
const ServerDetail = lazyPage(() =>
  import("./features/admin/Nodes").then((module) => ({
    default: module.ServerDetail,
  })),
);
const NodesPage = lazyPage(() =>
  import("./features/admin/Nodes").then((module) => ({
    default: module.NodesPage,
  })),
);
const NodeDetail = lazyPage(() =>
  import("./features/admin/Nodes").then((module) => ({
    default: module.NodeDetail,
  })),
);
const NodeCreate = lazyPage(() =>
  import("./features/admin/Nodes").then((module) => ({
    default: module.NodeCreate,
  })),
);
const PoolsPage = lazyPage(() =>
  import("./features/admin/Nodes").then((module) => ({
    default: module.PoolsPage,
  })),
);
const PlansPage = lazyPage(() =>
  import("./features/Plans").then((module) => ({ default: module.PlansPage })),
);
const PlanCreate = lazyPage(() =>
  import("./features/Plans").then((module) => ({ default: module.PlanCreate })),
);
const PlanDetail = lazyPage(() =>
  import("./features/Plans").then((module) => ({ default: module.PlanDetail })),
);
const StorePlans = lazyPage(() =>
  import("./features/Plans").then((module) => ({ default: module.StorePlans })),
);
const Checkout = lazyPage(() =>
  import("./features/Plans").then((module) => ({ default: module.Checkout })),
);
const OrdersPage = lazyPage(() =>
  import("./features/Orders").then((module) => ({
    default: module.OrdersPage,
  })),
);
const OrderDetail = lazyPage(() =>
  import("./features/Orders").then((module) => ({
    default: module.OrderDetail,
  })),
);
const TicketsPage = lazyPage(() =>
  import("./features/Tickets").then((module) => ({
    default: module.TicketsPage,
  })),
);
const TicketDetail = lazyPage(() =>
  import("./features/Tickets").then((module) => ({
    default: module.TicketDetail,
  })),
);
const Subscriptions = lazyPage(() =>
  import("./features/portal/Subscriptions").then((module) => ({
    default: module.Subscriptions,
  })),
);
const Account = lazyPage(() =>
  import("./features/portal/Account").then((module) => ({
    default: module.Account,
  })),
);
const Gifts = lazyPage(() =>
  import("./features/portal/Account").then((module) => ({
    default: module.Gifts,
  })),
);
const Help = lazyPage(() =>
  import("./features/portal/Account").then((module) => ({
    default: module.Help,
  })),
);
const HelpArticle = lazyPage(() =>
  import("./features/portal/Account").then((module) => ({
    default: module.HelpArticle,
  })),
);
const Referrals = lazyPage(() =>
  import("./features/portal/Account").then((module) => ({
    default: module.Referrals,
  })),
);
const ContentPages = lazyPage(() =>
  import("./features/admin/Content").then((module) => ({
    default: module.ContentPages,
  })),
);
const ContentEditor = lazyPage(() =>
  import("./features/admin/Content").then((module) => ({
    default: module.ContentEditor,
  })),
);
function Require({
  permission,
  children,
}: {
  permission: string;
  children: ReactNode;
}) {
  const { can } = useAuth();
  return can(permission) ? (
    children
  ) : (
    <Result status="403" title="没有执行此操作的权限" />
  );
}

function RouteError() {
  const error = useRouteError();
  return (
    <Result
      status="error"
      title="页面暂时无法显示"
      subTitle={
        error instanceof Error ? error.message : "数据或页面加载异常，请重试。"
      }
      extra={<Button onClick={() => location.reload()}>重新加载</Button>}
    />
  );
}
const common = [
  { index: true, element: <Navigate to="/overview" replace /> },
  { path: "overview", element: <Overview /> },
  { path: "orders", element: <OrdersPage /> },
  { path: "orders/:id", element: <OrderDetail /> },
];
const admin = [
  { path: "appearance", element: <Require permission="platform.appearance.read"><AppearancePage /></Require> },
  { path: "user-groups", element: <Require permission="iam.user.read"><UserGroupsPage /></Require> },
  { path: "routing", element: <Require permission="node.read"><RoutingPage /></Require> },
  { path: "commission-settings", element: <Require permission="billing.order.read"><CommissionSettings /></Require> },
  { path: "users", element: <UsersPage /> },
  { path: "users/:id", element: <UserDetail /> },
  { path: "servers", element: <ServersPage /> },
  { path: "servers/:id", element: <ServerDetail /> },
  { path: "nodes", element: <NodesPage /> },
  {
    path: "nodes/new",
    element: (
      <Require permission="node.provision">
        <NodeCreate />
      </Require>
    ),
  },
  { path: "node-pools", element: <Require permission="node.read"><PoolsPage /></Require> },
  // Keep previously shared and bookmarked URLs working after the menu migration.
  { path: "nodes/pools", element: <Navigate to="/node-pools" replace /> },
  { path: "nodes/:id", element: <NodeDetail /> },
  { path: "plans", element: <PlansPage /> },
  {
    path: "plans/new",
    element: (
      <Require permission="catalog.write">
        <PlanCreate />
      </Require>
    ),
  },
  { path: "plans/:id", element: <PlanDetail /> },
  { path: "tickets", element: <TicketsPage /> },
  { path: "tickets/:id", element: <TicketDetail /> },
  { path: "bulk", element: <BulkPage /> },
  { path: "notifications", element: <NotificationSettings /> },
  { path: "telegram", element: <TelegramSettings /> },
  { path: "content", element: <ContentPages /> },
  { path: "content/new", element: <ContentEditor /> },
  { path: "content/:id", element: <ContentEditor /> },
  ...[
    "device-limits",
    "traffic-resets",
    "late-payments",
    "coupons",
    "gift-cards",
    "commissions",
    "payment-providers",
    "announcements",
    "plugins",
    "risk",
    "switches",
    "audit",
  ].map((path) => ({ path, element: path === "coupons" ? <CouponsPage /> : path === "gift-cards" ? <GiftCardsPage /> : path === "plugins" ? <PluginsPage /> : <OperationsPage screen={path} /> })),
];
const portal = [
  { path: "subscriptions", element: <Subscriptions /> },
  { path: "plans", element: <StorePlans /> },
  { path: "plans/:id/checkout", element: <Checkout /> },
  { path: "support", element: <TicketsPage /> },
  { path: "support/:id", element: <TicketDetail /> },
  { path: "gifts", element: <Gifts /> },
  { path: "help", element: <Help /> },
  { path: "help/:slug", element: <HelpArticle /> },
  { path: "account", element: <Account /> },
  { path: "referrals", element: <Referrals /> },
];
const router = createHashRouter([
  {
    element: <Gateway />,
    errorElement: <RouteError />,
    children: [
      ...common,
      ...(runtime.domain === "admin" ? admin : portal),
      {
        path: "*",
        element: (
          <Result
            status="404"
            title="页面不存在"
            extra={<a href="#/overview">返回概览</a>}
          />
        ),
      },
    ],
  },
]);
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 20_000, retry: false, refetchOnWindowFocus: true },
    mutations: { retry: false },
  },
});
ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: "#4263df",
          borderRadius: 10,
          colorBgLayout: "#f5f7fb",
          colorText: "#263248",
          fontFamily:
            'Inter, "PingFang SC", "Microsoft YaHei", system-ui, sans-serif',
        },
        components: {
          Menu: {
            itemHeight: 42,
            itemSelectedBg: "#edf1ff",
            itemSelectedColor: "#3153cb",
          },
          Card: { headerFontSize: 15 },
          Table: { headerBg: "#f7f9fc" },
        },
      }}
    >
      <App>
        <QueryClientProvider client={queryClient}>
          <DialogHost>
            <AuthProvider>
              <PortalAppearance><RouterProvider router={router} /></PortalAppearance>
            </AuthProvider>
          </DialogHost>
        </QueryClientProvider>
      </App>
    </ConfigProvider>
  </React.StrictMode>,
);
