import { it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { RevenueChart } from "../src/features/admin/DashboardAnalytics";
it("distinguishes absent data from zero revenue", () => {
 const view = render(<RevenueChart points={[]} currency="CNY" field="displayed_net" />);
 expect(screen.getByText("所选时间范围暂无收入数据")).toBeInTheDocument();
 view.rerender(<RevenueChart points={[{ date: "2026-09-07", displayed_net: 0, actual_credit: 0, actual_debit: 0, adjustment: 0 }]} currency="CNY" field="displayed_net" />);
 expect(screen.getByRole("img", { name: "每日收入折线图" })).toBeInTheDocument();
 expect(screen.getByText("展示净额 CNY 0.00")).toBeInTheDocument();
});
it("keeps signed amounts exact and rejects unsafe numeric amounts", () => {
 const view = render(<RevenueChart points={[{ date: "2026-09-07", displayed_net: "-123", actual_credit: "200", actual_debit: "323", adjustment: 0 }]} currency="USD" field="displayed_net" />);
 expect(screen.getByText("展示净额 USD -1.23")).toBeInTheDocument();
 expect(screen.getByText("入账 USD 2.00")).toBeInTheDocument();
 view.rerender(<RevenueChart points={[{ date: "2026-09-07", displayed_net: Number.MAX_SAFE_INTEGER + 1 }]} currency="USD" field="displayed_net" />);
 expect(screen.getByText("部分金额无法准确解析，请查看收入明细")).toBeInTheDocument();
});
