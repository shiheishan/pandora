import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiClient, failure } from "./api";
import {
  loginSchema,
  principalSchema,
  principalSubject,
  type Principal,
} from "./data";
import { runtime, tokenKey } from "./runtime";
import { useDialog } from "./dialogs";

type Auth = {
  api: ApiClient;
  token: string;
  principal: Principal | null;
  ready: boolean;
  sessionError: string;
  scope: string;
  can: (permission?: string) => boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
};
const Context = createContext<Auth | null>(null);
export function useAuth(): Auth {
  const value = useContext(Context);
  if (!value) throw new Error("AuthProvider missing");
  return value;
}
const readToken = () => {
  try {
    return localStorage.getItem(tokenKey) || "";
  } catch {
    return "";
  }
};

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState(readToken);
  const tokenRef = useRef(token);
  const [principal, setPrincipal] = useState<Principal | null>(null);
  const [ready, setReady] = useState(false);
  const [sessionError, setSessionError] = useState("");
  const [epoch, setEpoch] = useState(0);
  const queryClient = useQueryClient();
  const open = useDialog();
  const flight = useRef<Promise<boolean> | null>(null);
  const generation = useRef(0);
  const setAccessToken = useCallback((value: string) => {
    tokenRef.current = value;
    setToken(value);
    try {
      if (value) localStorage.setItem(tokenKey, value);
      else localStorage.removeItem(tokenKey);
    } catch {
      /* Session still works in memory. */
    }
  }, []);
  const clear = useCallback(() => {
    generation.current++;
    setAccessToken("");
    setPrincipal(null);
    setReady(true);
    setSessionError("");
    setEpoch((value) => value + 1);
    queryClient.clear();
    window.dispatchEvent(new Event("pandora:logout"));
    try {
      for (let i = sessionStorage.length - 1; i >= 0; i--) {
        const key = sessionStorage.key(i);
        if (key?.startsWith(`pandora:${runtime.domain}:draft:`))
          sessionStorage.removeItem(key);
      }
    } catch {
      /* Storage may be unavailable. */
    }
  }, [queryClient, setAccessToken]);
  const apiRef = useRef<ApiClient | null>(null);
  const reauth = useCallback(() => {
    if (flight.current) return flight.current;
    if (runtime.domain !== "admin" || !tokenRef.current)
      return Promise.resolve(false);
    const controller = new AbortController();
    const current = generation.current;
    const promise = open({
      title: "确认你的身份",
      description: "这项操作需要近期身份确认。取消后输入会保留。",
      submitLabel: "确认身份",
      fields: [
        {
          name: "password",
          label: "当前密码",
          type: "password",
          required: true,
        },
      ],
      onSubmit: async (values) => {
        const result = await apiRef.current!.request(
          "v1/auth/reauth",
          loginSchema,
          {
            method: "POST",
            body: values,
            skipReauth: true,
            signal: controller.signal,
          },
        );
        if (controller.signal.aborted || current !== generation.current) return;
        setAccessToken(result.access_token);
      },
    }).finally(() => {
      controller.abort();
      if (flight.current === promise) flight.current = null;
    });
    flight.current = promise;
    return promise;
  }, [open, setAccessToken]);
  const api = useMemo(
    () =>
      new ApiClient(
        runtime.apiBase,
        () => tokenRef.current,
        clear,
        reauth,
        () => generation.current,
        () => { void queryClient.invalidateQueries({ queryKey: [runtime.domain], refetchType: "active" }); },
      ),
    [clear, reauth, queryClient],
  );
  apiRef.current = api;
  useEffect(() => {
    if (!tokenRef.current) {
      setReady(true);
      return;
    }
    const controller = new AbortController();
    const current = ++generation.current;
    void api
      .request("v1/me", principalSchema, { signal: controller.signal })
      .then((value) => {
        if (current === generation.current) {
          setPrincipal(value);
          setSessionError("");
        }
      })
      .catch((value) => {
        if (
          current === generation.current &&
          failure(value).kind !== "cancelled"
        )
          setSessionError(failure(value).message);
      })
      .finally(() => {
        if (current === generation.current) setReady(true);
      });
    return () => controller.abort();
  }, [api]);
  const login = async (email: string, password: string) => {
    const current = ++generation.current;
    setSessionError("");
    const result = await api.request("v1/auth/login", loginSchema, {
      method: "POST",
      body: { email, password },
      anonymous: true,
      skipReauth: true,
    });
    setAccessToken(result.access_token);
    const me = await api.request("v1/me", principalSchema);
    if (current !== generation.current) return;
    queryClient.clear();
    setPrincipal(me);
    setReady(true);
    setEpoch((value) => value + 1);
  };
  const logout = async () => {
    try {
      await api.write("v1/auth/logout", {});
    } catch {
      /* Always clear this browser session. */
    } finally {
      clear();
    }
  };
  const permissions = principal?.permissions || [];
  const scope = `${runtime.domain}:${principalSubject(principal) || "anonymous"}:${epoch}`;
  return (
    <Context.Provider
      value={{
        api,
        token,
        principal,
        ready,
        sessionError,
        scope,
        login,
        logout,
        can: (permission) =>
          !permission ||
          permissions.includes(permission) ||
          permissions.includes("*"),
      }}
    >
      {children}
    </Context.Provider>
  );
}
