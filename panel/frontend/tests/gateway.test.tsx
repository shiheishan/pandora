import { it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
const state = vi.hoisted(() => ({ ready: true, principal: null as null | { email: string }, loaded: vi.fn() }));
vi.mock("../src/core/auth", () => ({ useAuth: () => state }));
vi.mock("../src/app/Login", () => ({ Login: () => <p>登录入口</p> }));
vi.mock("../src/app/Frame", () => { state.loaded(); return { Frame: () => <p>完整工作台</p> }; });
import { Gateway } from "../src/app/Gateway";
it("defers workspace loading until authentication is ready and succeeds", async () => {
  const view = render(<Gateway />);
  expect(screen.getByText("登录入口")).toBeInTheDocument(); expect(state.loaded).not.toHaveBeenCalled();
  state.ready = false; view.rerender(<Gateway />);
  expect(screen.getByRole("status")).toBeInTheDocument(); expect(state.loaded).not.toHaveBeenCalled();
  state.ready = true; state.principal = { email: "test@example.test" }; view.rerender(<Gateway />);
  expect(await screen.findByText("完整工作台")).toBeInTheDocument();
  state.principal = null; view.rerender(<Gateway />); expect(screen.getByText("登录入口")).toBeInTheDocument();
});
