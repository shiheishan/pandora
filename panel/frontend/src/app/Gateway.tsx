import { lazy, Suspense } from "react";
import { Skeleton } from "antd";
import { useAuth } from "../core/auth";
import { Login } from "./Login";

const Workspace = lazy(() => import("./Frame").then(module => ({ default: module.Frame })));
export function Gateway() {
  const { principal, ready } = useAuth();
  const loading = <div className="initial-loading" role="status" aria-label="正在加载工作台"><Skeleton active /></div>;
  if (!ready) return loading;
  if (!principal) return <Login />;
  return <Suspense fallback={loading}><Workspace /></Suspense>;
}
