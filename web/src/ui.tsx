import { Component, createContext, useCallback, useContext, useEffect, useId, useRef, useState, type ErrorInfo, type ReactNode } from "react";
import { Icon, type IconName } from "./icons";
import { useT } from "./i18n";
import { copyText, isErrStatus } from "./util";

/* ---------- Toasts ---------- */
type ToastKind = "ok" | "err" | "info";
interface ToastItem {
  id: number;
  kind: ToastKind;
  title: string;
  body?: string;
}
type ToastFn = (t: Omit<ToastItem, "id">) => void;
const ToastCtx = createContext<ToastFn>(() => {});
export const useToast = () => useContext(ToastCtx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const seq = useRef(0);
  const dismiss = useCallback((id: number) => setItems((xs) => xs.filter((x) => x.id !== id)), []);
  const push = useCallback<ToastFn>(
    (t) => {
      const id = ++seq.current;
      setItems((xs) => [...xs.slice(-3), { ...t, id }]);
      window.setTimeout(() => dismiss(id), t.kind === "err" ? 8000 : 4500);
    },
    [dismiss],
  );
  const icon: Record<ToastKind, IconName> = { ok: "check", err: "alert", info: "info" };
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={`toast ${t.kind}`}>
            <Icon name={icon[t.kind]} className="t-icon" size={18} style={{ flex: "none", marginTop: 1 }} />
            <div className="grow">
              <div className="t-title">{t.title}</div>
              {t.body && <div className="t-body">{t.body}</div>}
            </div>
            <button className="btn ghost icon sm" aria-label="Dismiss" onClick={() => dismiss(t.id)}>
              <Icon name="x" size={14} />
            </button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

/* ---------- Confirm ---------- */
interface ConfirmOpts {
  title: string;
  body?: ReactNode;
  confirm?: string;
  danger?: boolean;
}
type ConfirmFn = (o: ConfirmOpts) => Promise<boolean>;
const ConfirmCtx = createContext<ConfirmFn>(async () => false);
export const useConfirm = () => useContext(ConfirmCtx);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const t = useT();
  const [state, setState] = useState<(ConfirmOpts & { resolve: (v: boolean) => void }) | null>(null);
  const confirm = useCallback<ConfirmFn>((o) => new Promise<boolean>((resolve) => setState({ ...o, resolve })), []);
  const close = (v: boolean) => {
    state?.resolve(v);
    setState(null);
  };
  const okRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!state) return;
    const prev = document.activeElement as HTMLElement | null;
    okRef.current?.focus();
    return () => prev?.focus?.();
  }, [state]);
  return (
    <ConfirmCtx.Provider value={confirm}>
      {children}
      {state && (
        <>
          <div className="overlay dialog-overlay" onClick={() => close(false)} />
          <div
            className="dialog"
            role="alertdialog"
            aria-modal="true"
            aria-labelledby="confirm-title"
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.stopPropagation();
                close(false);
              }
            }}
          >
            <h2 id="confirm-title" className="row">
              {state.danger && <Icon name="alert" style={{ color: "var(--err)" }} />}
              {state.title}
            </h2>
            {state.body && <div className="muted" style={{ marginTop: 8, fontSize: 13 }}>{state.body}</div>}
            <div className="row" style={{ justifyContent: "flex-end", marginTop: 18 }}>
              <button className="btn" onClick={() => close(false)}>
                {t("common.cancel")}
              </button>
              <button ref={okRef} className={`btn ${state.danger ? "danger solid" : "primary"}`} onClick={() => close(true)}>
                {state.confirm || t("common.confirm")}
              </button>
            </div>
          </div>
        </>
      )}
    </ConfirmCtx.Provider>
  );
}

/* ---------- Drawer ---------- */
export function Drawer({ open, onClose, title, children, footer, width }: { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; footer?: ReactNode; width?: number }) {
  const ref = useRef<HTMLDivElement>(null);
  const t = useT();
  useEffect(() => {
    if (!open) return;
    const prev = document.activeElement as HTMLElement | null;
    const el = ref.current;
    const first = el?.querySelector<HTMLElement>("input, select, textarea, button:not(.drawer-close)");
    (first || el)?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !document.querySelector(".dialog")) onClose();
      if (e.key === "Tab" && el) {
        const f = Array.from(el.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), input:not(:disabled), select, textarea, [tabindex]:not([tabindex="-1"])'));
        if (!f.length) return;
        const a = f[0];
        const z = f[f.length - 1];
        if (e.shiftKey && document.activeElement === a) {
          e.preventDefault();
          z.focus();
        } else if (!e.shiftKey && document.activeElement === z) {
          e.preventDefault();
          a.focus();
        }
      }
    };
    document.addEventListener("keydown", onKey);
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = "";
      prev?.focus?.();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  if (!open) return null;
  return (
    <>
      <div className="overlay" onClick={onClose} />
      <div className="drawer" role="dialog" aria-modal="true" aria-label={typeof title === "string" ? title : undefined} ref={ref} tabIndex={-1} style={width ? { width: `min(${width}px, 100vw)` } : undefined}>
        <div className="drawer-head">
          <h2 className="grow ellipsis">{title}</h2>
          <button className="btn ghost icon drawer-close" onClick={onClose} aria-label={t("common.close")}>
            <Icon name="x" />
          </button>
        </div>
        <div className="drawer-body">{children}</div>
        {footer && <div className="drawer-foot">{footer}</div>}
      </div>
    </>
  );
}

/* ---------- Small pieces ---------- */
export type Tone = "ok" | "warn" | "err" | "accent" | "neutral";
export function Badge({ tone = "neutral", icon, children, title }: { tone?: Tone; icon?: IconName; children: ReactNode; title?: string }) {
  return (
    <span className={`badge ${tone === "neutral" ? "" : tone}`} title={title}>
      {icon && <Icon name={icon} size={12} strokeWidth={2.5} />}
      {children}
    </span>
  );
}

export function StatusCode({ status }: { status: number }) {
  if (status === 0) return <Badge icon="clock">…</Badge>;
  if (status === 499) return <Badge tone="warn" icon="x" title="client closed request">499</Badge>;
  if (isErrStatus(status)) return <Badge tone="err" icon="alert">{status}</Badge>;
  return <Badge tone="ok" icon="check">{status}</Badge>;
}

export function Empty({ icon = "inbox", title, body, action }: { icon?: IconName; title: string; body?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      <div className="e-icon">
        <Icon name={icon} size={20} />
      </div>
      <h3>{title}</h3>
      {body && <p>{body}</p>}
      {action}
    </div>
  );
}

export function CopyButton({ text, label, className = "btn sm", iconOnly }: { text: string; label?: string; className?: string; iconOnly?: boolean }) {
  const t = useT();
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className={`${className} copy-btn`}
      aria-label={label || t("common.copy")}
      title={iconOnly ? (done ? t("common.copied") : label || t("common.copy")) : undefined}
      onClick={async () => {
        if (await copyText(text)) {
          setDone(true);
          window.setTimeout(() => setDone(false), 1400);
        }
      }}
    >
      <Icon name={done ? "check" : "copy"} size={13} />
      {!iconOnly && <span>{done ? t("common.copied") : label || t("common.copy")}</span>}
    </button>
  );
}

export function Field({ label, hint, error, children, id }: { label: ReactNode; hint?: ReactNode; error?: string; children: (id: string, describedBy?: string) => ReactNode; id?: string }) {
  const auto = useId();
  const fid = id || auto;
  const hid = hint || error ? `${fid}-desc` : undefined;
  return (
    <div className="field">
      <label htmlFor={fid}>{label}</label>
      {children(fid, hid)}
      {error ? (
        <div className="error" id={hid} role="alert">
          <Icon name="alert" size={12} />
          {error}
        </div>
      ) : hint ? (
        <div className="hint" id={hid}>
          {hint}
        </div>
      ) : null}
    </div>
  );
}

export function Switch({ checked, onChange, label, id }: { checked: boolean; onChange: (v: boolean) => void; label: ReactNode; id?: string }) {
  const auto = useId();
  const fid = id || auto;
  return (
    <label className="check" htmlFor={fid}>
      <span className="switch">
        <input id={fid} type="checkbox" role="switch" checked={checked} onChange={(e) => onChange(e.target.checked)} />
        <span />
      </span>
      <span>{label}</span>
    </label>
  );
}

export function CodeBlock({ code, label }: { code: string; label?: string }) {
  return (
    <div className="code-wrap">
      <pre className="code" aria-label={label} tabIndex={0}>
        {code}
      </pre>
      <CopyButton text={code} />
    </div>
  );
}

export function ErrorNote({ error }: { error?: Error | string }) {
  if (!error) return null;
  return (
    <div className="alert err" role="alert">
      <Icon name="alert" style={{ flex: "none", marginTop: 2 }} />
      <pre>{typeof error === "string" ? error : error.message}</pre>
    </div>
  );
}

/* ---------- Error boundary ---------- */
interface EBState {
  error?: Error;
}
export class ErrorBoundary extends Component<{ children: ReactNode; resetKey?: string }, EBState> {
  state: EBState = {};
  static getDerivedStateFromError(error: Error): EBState {
    return { error };
  }
  componentDidCatch(error: Error, info: ErrorInfo) {
    void error;
    void info;
  }
  componentDidUpdate(prev: { resetKey?: string }) {
    if (prev.resetKey !== this.props.resetKey && this.state.error) this.setState({ error: undefined });
  }
  render() {
    if (this.state.error) {
      return (
        <div className="card card-pad" style={{ margin: 20 }}>
          <div className="alert err">
            <Icon name="alert" style={{ flex: "none", marginTop: 2 }} />
            <div>
              <strong>Something broke while rendering this page / Trang gặp lỗi khi hiển thị.</strong>
              <pre style={{ marginTop: 6 }}>{this.state.error.message}</pre>
            </div>
          </div>
          <button className="btn" style={{ marginTop: 12 }} onClick={() => this.setState({ error: undefined })}>
            <Icon name="refresh" size={14} /> Retry / Thử lại
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}

/* ---------- Meter ---------- */
/** Progress bar with a value line. pct is 0–100; tone escalates at 80% / 100%. */
export function Meter({ pct, value, sub, label }: { pct: number; value: ReactNode; sub?: ReactNode; label: string }) {
  const p = Math.max(0, Math.min(100, pct));
  const tone = pct >= 100 ? "err" : pct >= 80 ? "warn" : "";
  return (
    <div className="meter-block">
      <div className="meter-line">
        <span className="ml-val">{value}</span>
        {sub && <span className="ml-sub">{sub}</span>}
      </div>
      <div className={`meter ${tone}`} role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(p)}>
        <i style={{ width: `${p}%` }} />
      </div>
    </div>
  );
}
