import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { useRealtime } from "../src/core/realtime";

const identity = vi.hoisted(() => ({
  token: "token-A",
  scope: "admin:user-A:0",
  can: () => true,
}));
vi.mock("../src/core/auth", () => ({ useAuth: () => identity }));

function harness() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  };
}

describe("authenticated stream ownership", () => {
  it("coalesces events and cancels stream and queued refresh on unmount", async () => {
    vi.useFakeTimers();
    identity.token = "token-A";
    identity.scope = "admin:user-A:0";
    let stream!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        stream = controller;
      },
    });
    const fetcher = vi.fn(
      async () =>
        new Response(body, {
          headers: { "Content-Type": "text/event-stream" },
        }),
    );
    vi.stubGlobal("fetch", fetcher);
    const { client, wrapper } = harness();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const hook = renderHook(() => useRealtime(), { wrapper });
    await act(async () => {
      await Promise.resolve();
    });
    expect(hook.result.current).toBe("connected");
    await act(async () => {
      stream.enqueue(new TextEncoder().encode("data: one\n\ndata: two\n\n"));
      await Promise.resolve();
      await vi.advanceTimersByTimeAsync(400);
    });
    expect(invalidate).toHaveBeenCalledTimes(1);
    const options = invalidate.mock.calls[0]?.[0];
    expect(
      options?.predicate?.({ queryKey: ["admin", "admin:user-A:0"] } as never),
    ).toBe(true);
    expect(
      options?.predicate?.({ queryKey: ["admin", "admin:user-B:0"] } as never),
    ).toBe(false);
    await act(async () => {
      stream.enqueue(new TextEncoder().encode("data: late\n\n"));
      await Promise.resolve();
    });
    hook.unmount();
    const request = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(request[1].signal?.aborted).toBe(true);
    await act(async () => {
      stream.close();
      await vi.advanceTimersByTimeAsync(60000);
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledTimes(1);
  });

  it("ignores a late successful stream response after the identity changes", async () => {
    identity.token = "token-A";
    identity.scope = "admin:user-A:0";
    let resolveOld!: (value: Response) => void;
    const fetcher = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveOld = resolve;
          }),
      )
      .mockResolvedValueOnce(new Response("", { status: 403 }));
    vi.stubGlobal("fetch", fetcher);
    const { client, wrapper } = harness();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const hook = renderHook(() => useRealtime(), { wrapper });
    identity.token = "token-B";
    identity.scope = "admin:user-B:1";
    await act(async () => {
      hook.rerender();
    });
    expect(hook.result.current).toBe("paused");
    await act(async () => {
      resolveOld(new Response("data: stale\n\n"));
      await Promise.resolve();
    });
    expect(hook.result.current).toBe("paused");
    expect(invalidate).not.toHaveBeenCalled();
    expect(fetcher.mock.calls[0]?.[1].signal.aborted).toBe(true);
    hook.unmount();
  });
});
