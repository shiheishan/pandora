import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useAuth } from "./auth";
import { apiUrl, runtime } from "./runtime";

export function useRealtime() {
  const { token, scope, can } = useAuth();
  const client = useQueryClient();
  const [status, setStatus] = useState<"connected" | "reconnecting" | "paused">(
    "paused",
  );
  useEffect(() => {
    if (
      !token ||
      (runtime.domain === "admin" && !can("ops.notification.read"))
    ) {
      setStatus("paused");
      return;
    }
    const controller = new AbortController();
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let refreshTimer: ReturnType<typeof setTimeout> | undefined;
    let failures = 0;
    const invalidate = () => {
      if (refreshTimer) return;
      refreshTimer = setTimeout(() => {
        refreshTimer = undefined;
        if (!controller.signal.aborted)
          void client.invalidateQueries({
            predicate: (query) => query.queryKey[1] === scope,
            refetchType: "active",
          });
      }, 400);
    };
    const connect = async () => {
      if (controller.signal.aborted) return;
      setStatus("reconnecting");
      try {
        const result = await fetch(apiUrl(runtime.apiBase, "v1/events"), {
          headers: {
            Authorization: `Bearer ${token}`,
            Accept: "text/event-stream",
          },
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        if (result.status === 401 || result.status === 403) {
          setStatus("paused");
          return;
        }
        if (!result.ok || !result.body) throw new Error("stream unavailable");
        setStatus("connected");
        failures = 0;
        invalidate();
        const reader = result.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        try {
          while (!controller.signal.aborted) {
            const part = await reader.read();
            if (controller.signal.aborted) break;
            if (part.done) break;
            buffer += decoder
              .decode(part.value, { stream: true })
              .replace(/\r\n/g, "\n");
            let end: number;
            while ((end = buffer.indexOf("\n\n")) >= 0) {
              const frame = buffer.slice(0, end);
              buffer = buffer.slice(end + 2);
              if (frame.split("\n").some((line) => line.startsWith("data:")))
                invalidate();
            }
            if (buffer.length > 1_048_576)
              throw new Error("stream frame too large");
          }
        } finally {
          reader.releaseLock();
        }
      } catch {
        /* Disconnection is shown as state, never as a successful refresh. */
      }
      if (!controller.signal.aborted) {
        setStatus("reconnecting");
        retryTimer = setTimeout(
          () => void connect(),
          Math.min(30_000, 1000 * 2 ** Math.min(failures++, 5)) +
            Math.random() * 500,
        );
      }
    };
    void connect();
    return () => {
      controller.abort();
      clearTimeout(retryTimer);
      clearTimeout(refreshTimer);
    };
    // The authenticated scope and token own exactly one stream instance.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, scope, client]);
  return status;
}
