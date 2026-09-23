import {
  Component,
  lazy,
  Suspense,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { Row } from "./data";

export type FormField = {
  name: string;
  label: string;
  type?:
    "text" | "password" | "textarea" | "number" | "select" | "switch" | "date" | "datetime";
  required?: boolean;
  options?: { label: string; value: string | number }[];
  help?: string;
  placeholder?: string;
  min?: number;
  max?: number;
  disabled?: boolean;
  multiple?: boolean;
  group?: string;
};
type Options = {
  title: string;
  description?: ReactNode;
  fields?: FormField[];
  initial?: Row;
  submitLabel?: string;
  danger?: boolean;
  width?: number;
  protectDraft?: boolean;
  onSubmit?: (values: Row) => Promise<unknown>;
  onValuesChange?: (values: Row) => void;
};
export type Entry = Options & { id: number; resolve: (value: boolean) => void };
const Context = createContext<(options: Options) => Promise<boolean>>(() =>
  Promise.resolve(false),
);
export const useDialog = () => useContext(Context);

class DialogLoadBoundary extends Component<{ children: ReactNode; close: () => void }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() {
    return this.state.failed ? <div role="alert" className="dialog-loading">
      操作窗口加载失败，尚未提交操作。请刷新页面后重试。
      <button onClick={this.props.close}>关闭</button>
    </div> : this.props.children;
  }
}
const FormDialog = lazy(() => import("./FormDialog"));

export function DialogHost({ children }: { children: ReactNode }) {
  const [entries, setEntries] = useState<Entry[]>([]);
  const live = useRef(new Map<number, Entry>());
  const next = useRef(0);
  const settle = useCallback((id: number, result: boolean) => {
    const entry = live.current.get(id);
    if (!entry) return;
    live.current.delete(id);
    entry.resolve(result);
    setEntries((current) => current.filter((item) => item.id !== id));
  }, []);
  const open = useCallback(
    (options: Options) =>
      new Promise<boolean>((resolve) => {
        const entry = { ...options, resolve, id: ++next.current };
        live.current.set(entry.id, entry);
        setEntries((current) => [...current, entry]);
      }),
    [],
  );
  useEffect(
    () => () => {
      for (const entry of live.current.values()) entry.resolve(false);
      live.current.clear();
    },
    [],
  );
  useEffect(() => {
    const close = () => {
      for (const id of live.current.keys()) settle(id, false);
    };
    window.addEventListener("hashchange", close);
    window.addEventListener("pandora:logout", close);
    return () => {
      window.removeEventListener("hashchange", close);
      window.removeEventListener("pandora:logout", close);
    };
  }, [settle]);
  return (
    <Context.Provider value={open}>
      {children}
      <Suspense fallback={<div role="status" className="dialog-loading">正在加载操作窗口…</div>}>
      {entries.map((entry, index) => (
        <DialogLoadBoundary key={entry.id} close={() => settle(entry.id, false)}>
        <FormDialog
          entry={entry}
          active={index === entries.length - 1}
          settle={settle}
        />
        </DialogLoadBoundary>
      ))}
      </Suspense>
    </Context.Provider>
  );
}

