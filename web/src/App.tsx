import { Fragment, lazy, Suspense, useEffect, useState, type FormEvent } from "react";
import { NavLink, Navigate, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { getAuthState, getPassword, onAuthState, setPassword, type AuthState } from "./api";
import type { Key } from "./dict";
import { useStored } from "./hooks";
import { useI18n } from "./i18n";
import { Icon, type IconName } from "./icons";
import { ConfigProvider, StatusProvider, useStatus } from "./store";
import { ConfirmProvider, CopyButton, ErrorBoundary, ToastProvider } from "./ui";
import { endpointURL, fmtUptime } from "./util";
import Overview from "./pages/Overview";

const Providers = lazy(() => import("./pages/Providers"));
const Accounts = lazy(() => import("./pages/Accounts"));
const Combos = lazy(() => import("./pages/Combos"));
const Requests = lazy(() => import("./pages/Requests"));
const Integrations = lazy(() => import("./pages/Integrations"));
const Settings = lazy(() => import("./pages/Settings"));
const Playground = lazy(() => import("./pages/Playground"));

// Order defines the 1–8 keyboard shortcuts; groups are visual only.
const NAV: { to: string; key: Key; icon: IconName; group: Key }[] = [
  { to: "/", key: "nav.overview", icon: "overview", group: "navg.monitor" },
  { to: "/requests", key: "nav.requests", icon: "requests", group: "navg.monitor" },
  { to: "/providers", key: "nav.providers", icon: "providers", group: "navg.routing" },
  { to: "/accounts", key: "nav.accounts", icon: "accounts", group: "navg.routing" },
  { to: "/combos", key: "nav.combos", icon: "combos", group: "navg.routing" },
  { to: "/integrations", key: "nav.integrations", icon: "integrations", group: "navg.connect" },
  { to: "/playground", key: "nav.playground", icon: "playground", group: "navg.connect" },
  { to: "/settings", key: "nav.settings", icon: "settings", group: "navg.system" },
];

type Theme = "system" | "light" | "dark";

function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useStored<Theme>("clr.theme", "system");
  useEffect(() => {
    const el = document.documentElement;
    if (theme === "system") delete el.dataset.theme;
    else el.dataset.theme = theme;
  }, [theme]);
  const cycle = () => {
    const sysDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
    const effective = theme === "system" ? (sysDark ? "dark" : "light") : theme;
    setTheme(effective === "dark" ? "light" : "dark");
  };
  return [theme, cycle];
}

function isTyping(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null;
  if (!el) return false;
  return el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName);
}

export default function App() {
  const [auth, setAuth] = useState<AuthState>(getAuthState());
  const [gen, setGen] = useState(0);
  useEffect(() => onAuthState(setAuth), []);
  return (
    <ToastProvider>
      <ConfirmProvider>
        {auth === "password" ? (
          <PasswordScreen onDone={() => setGen((g) => g + 1)} />
        ) : auth === "forbidden" ? (
          <ForbiddenScreen onRetry={() => { setPassword(getPassword()); setGen((g) => g + 1); }} />
        ) : (
          <StatusProvider key={`s${gen}`}>
            <ConfigProvider key={`c${gen}`}>
              <Shell />
            </ConfigProvider>
          </StatusProvider>
        )}
      </ConfirmProvider>
    </ToastProvider>
  );
}

function Shell() {
  const { t, lang, setLang } = useI18n();
  const [theme, cycleTheme] = useTheme();
  const [menu, setMenu] = useState(false);
  const loc = useLocation();
  const nav = useNavigate();
  const cur = NAV.find((n) => (n.to === "/" ? loc.pathname === "/" : loc.pathname.startsWith(n.to)));

  useEffect(() => setMenu(false), [loc.pathname]);
  useEffect(() => {
    document.title = cur ? `${t(cur.key)} · clirouter` : "clirouter";
  }, [cur, t]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isTyping(e) || document.querySelector(".drawer, .dialog")) return;
      const n = parseInt(e.key, 10);
      if (n >= 1 && n <= NAV.length) {
        e.preventDefault();
        nav(NAV[n - 1].to);
      } else if (e.key === "t") cycleTheme();
      else if (e.key === "l") setLang(lang === "vi" ? "en" : "vi");
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  return (
    <div className="app">
      <a href="#main" className="sr-only">
        {t("a11y.skip")}
      </a>
      {menu && <div className="backdrop" onClick={() => setMenu(false)} />}
      <aside className={`sidebar ${menu ? "open" : ""}`} aria-label={t("a11y.nav")}>
        <div className="brand">
          <span className="brand-logo">
            <Icon name="logo" size={15} strokeWidth={2.6} />
          </span>
          clirouter
        </div>
        <nav className="nav">
          {NAV.map((n, i) => (
            <Fragment key={n.to}>
              {(i === 0 || NAV[i - 1].group !== n.group) && <div className="nav-group">{t(n.group)}</div>}
              <NavLink to={n.to} end={n.to === "/"}>
                <Icon name={n.icon} />
                <span>{t(n.key)}</span>
                <kbd className="hide-sm">{i + 1}</kbd>
              </NavLink>
            </Fragment>
          ))}
        </nav>
        <div className="sidebar-foot">
          <EndpointCard />
          <div className="kbd-hint hide-sm">
            <kbd>1</kbd>–<kbd>8</kbd> {t("shell.kbdNav")} · <kbd>t</kbd> {t("shell.kbdTheme")} · <kbd>l</kbd> {t("shell.kbdLang")}
          </div>
        </div>
      </aside>
      <div className="main">
        <header className="topbar">
          <button className="btn ghost icon menu-btn" aria-label={t("a11y.menu")} aria-expanded={menu} onClick={() => setMenu((m) => !m)}>
            <Icon name="menu" />
          </button>
          <span className="crumbs ellipsis">
            <span className="hide-sm">clirouter</span>
            <Icon name="right" size={13} className="hide-sm" />
            <strong>{cur ? t(cur.key) : ""}</strong>
          </span>
          <span className="spacer" />
          <StatusPill />
          <button className="btn ghost sm" onClick={() => setLang(lang === "vi" ? "en" : "vi")} aria-label={t("shell.lang")} title={t("shell.lang")}>
            <Icon name="globe" size={14} />
            {lang === "vi" ? "VI" : "EN"}
          </button>
          <button className="btn ghost icon" onClick={cycleTheme} aria-label={t("shell.theme")} title={`${t("shell.theme")} (${theme})`}>
            <Icon name={theme === "dark" || (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches) ? "sun" : "moon"} />
          </button>
        </header>
        <main id="main" className="content" tabIndex={-1}>
          <ErrorBoundary resetKey={loc.pathname}>
            <Suspense fallback={<div className="faint">{t("common.loading")}</div>}>
              <Routes>
                <Route path="/" element={<Overview />} />
                <Route path="/providers" element={<Providers />} />
                <Route path="/accounts" element={<Accounts />} />
                <Route path="/combos" element={<Combos />} />
                <Route path="/requests" element={<Requests />} />
                <Route path="/integrations" element={<Integrations />} />
                <Route path="/settings" element={<Settings />} />
                <Route path="/playground" element={<Playground />} />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Routes>
            </Suspense>
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}

function EndpointCard() {
  const { t } = useI18n();
  const { status } = useStatus();
  if (!status) return null;
  const url = endpointURL(status.listen);
  return (
    <div className="endpoint-card">
      <div className="ep-label">
        <Icon name="integrations" size={12} />
        {t("shell.endpoint")}
      </div>
      <div className="ep-url">
        <code title={url}>{url}</code>
        <CopyButton text={url} label={t("common.copy")} className="btn ghost icon xs" iconOnly />
      </div>
    </div>
  );
}

function StatusPill() {
  const { t } = useI18n();
  const { status, error } = useStatus();
  if (error && !status) {
    return (
      <span className="pill" role="status">
        <span className="dot err" />
        <Icon name="alert" size={12} />
        {t("shell.offline")}
      </span>
    );
  }
  if (!status) {
    return (
      <span className="pill" aria-busy="true">
        <span className="dot" />
        <span className="skeleton" style={{ width: 90 }}>
          …
        </span>
      </span>
    );
  }
  const down = !!error;
  return (
    <span className="pill" role="status" title={`${status.listen} · ${status.config_path}`}>
      <span className={`dot ${down ? "err" : "ok"}`} />
      <span>{down ? t("shell.offline") : `v${status.version}`}</span>
      <span className="hide-sm faint">·</span>
      <span className="hide-sm">
        {t("shell.uptime")} {fmtUptime(status.uptime_s)}
      </span>
      <span className="faint">·</span>
      <span>
        {status.metrics.inflight} {t("shell.inflight")}
      </span>
    </span>
  );
}

function PasswordScreen({ onDone }: { onDone: () => void }) {
  const { t } = useI18n();
  const [pw, setPw] = useState("");
  const hadPw = !!getPassword();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setPassword(pw);
    onDone();
  };
  return (
    <div className="full-center">
      <form className="card auth-card stack" onSubmit={submit}>
        <div className="row">
          <span className="brand-logo">
            <Icon name="lock" size={14} />
          </span>
          <h1>{t("auth.title")}</h1>
        </div>
        <p className="muted">{t("auth.body")}</p>
        {hadPw && (
          <div className="alert err" role="alert">
            <Icon name="alert" style={{ flex: "none", marginTop: 2 }} />
            {t("auth.wrong")}
          </div>
        )}
        <div className="field">
          <label htmlFor="admin-pw">{t("auth.password")}</label>
          <input id="admin-pw" className="input" type="password" autoFocus autoComplete="current-password" value={pw} onChange={(e) => setPw(e.target.value)} />
        </div>
        <button className="btn primary" type="submit" disabled={!pw} style={{ width: "100%" }}>
          {t("auth.submit")}
        </button>
      </form>
    </div>
  );
}

function ForbiddenScreen({ onRetry }: { onRetry: () => void }) {
  const { t } = useI18n();
  return (
    <div className="full-center">
      <div className="card auth-card stack">
        <div className="row">
          <Icon name="lock" size={20} />
          <h1>{t("forbidden.title")}</h1>
        </div>
        <p className="muted">{t("forbidden.body")}</p>
        <pre className="code">{`# config.yaml\nadmin_password: "env:CLIROUTER_ADMIN_PASSWORD"`}</pre>
        <p className="muted small">{t("forbidden.tunnel")}</p>
        <pre className="code">ssh -L 8317:127.0.0.1:8317 user@server</pre>
        <button className="btn" onClick={onRetry}>
          <Icon name="refresh" size={14} /> {t("common.retry")}
        </button>
      </div>
    </div>
  );
}
