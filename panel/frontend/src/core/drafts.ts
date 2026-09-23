import { useCallback, useEffect, useState } from "react";
import { runtime } from "./runtime";

export function useDraft(subject: string, entity: string) {
  const key = `pandora:${runtime.domain}:draft:${subject}:${entity}:1`;
  const read = useCallback(() => {
    try {
      const saved: unknown = JSON.parse(sessionStorage.getItem(key) || "null");
      if (
        saved &&
        typeof saved === "object" &&
        "at" in saved &&
        typeof saved.at === "number" &&
        Date.now() - saved.at < 86_400_000 &&
        "text" in saved &&
        typeof saved.text === "string"
      )
        return saved.text;
    } catch {
      /* Use the in-memory draft. */
    }
    return "";
  }, [key]);
  const [state, setState] = useState(() => ({ key, value: read() }));
  const [persisted, setPersisted] = useState(true);
  const value = state.key === key ? state.value : read();
  const setValue = useCallback(
    (next: string) => {
      setState({ key, value: next });
      try {
        if (next)
          sessionStorage.setItem(
            key,
            JSON.stringify({ at: Date.now(), text: next }),
          );
        else sessionStorage.removeItem(key);
        setPersisted(true);
      } catch {
        setPersisted(false);
      }
    },
    [key],
  );
  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (value) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [value]);
  const clear = () => {
    setValue("");
    try {
      sessionStorage.removeItem(key);
    } catch {
      /* Cleared in memory. */
    }
  };
  return { value, setValue, clear, persisted };
}
