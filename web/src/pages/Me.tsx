import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Key } from "../dict";
import { useI18n } from "../i18n";
import { Icon } from "../icons";
import type { KeyInfo, KeyStatus } from "../types";
import { Badge, CopyButton, Meter, type Tone } from "../ui";
import { fmtNum, fmtUsd, toDateInput } from "../util";

const STORE = "clr.meKey";
const TONE: Record<KeyStatus, Tone> = { active: "ok", disabled: "neutral", expired: "warn", over_budget: "err" };

function loadKey(): string {
  try {
    return sessionStorage.getItem(STORE) || "";
  } catch {
    return "";
  }
}

function storeKey(k: string) {
  try {
    if (k) sessionStorage.setItem(STORE, k);
    else sessionStorage.removeItem(STORE);
  } catch {
    /* storage unavailable */
  }
}

/** Self-service usage page: authenticates with the user's client key only. */
export default function MePage() {
  const { t, lang, setLang } = useI18n();
  const [key, setKey] = useState(loadKey);
  const [input, setInput] = useState("");
  const [info, setInfo] = useState<KeyInfo>();
  const [err, setErr] = useState<string>();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    document.title = `${t("me.title")} · clirouter`;
  }, [t]);

  const fetchInfo = useCallback(async (k: string) => {
    setBusy(true);
    setErr(undefined);
    try {
      const res = await fetch("/v1/key/info", { headers: { Authorization: `Bearer ${k}`, Accept: "application/json" } });
      const text = await res.text();
      let body: unknown = null;
      try {
        body = text ? JSON.parse(text) : null;
      } catch {
        body = null;
      }
      if (!res.ok) {
        const e = (body as { error?: { message?: string } | string } | null)?.error;
        const msg = typeof e === "string" ? e : e?.message;
        setInfo(undefined);
        setErr(`HTTP ${res.status}${msg ? `: ${msg}` : ""}`);
        return;
      }
      setInfo(body as KeyInfo);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    if (key) fetchInfo(key);
  }, [key, fetchInfo]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const k = input.trim();
    if (!k) return;
    storeKey(k);
    setKey(k);
    setInput("");
  };

  const forget = () => {
    storeKey("");
    setKey("");
    setInfo(undefined);
    setErr(undefined);
  };

  const base = window.location.origin;

  return (
    <div className="me-page">
      <div className="me-wrap">
        <div className="me-head">
          <span className="brand-logo">
            <Icon name="logo" size={15} strokeWidth={2.6} />
          </span>
          <strong style={{ fontSize: 16 }}>clirouter</strong>
          <span className="spacer" />
          <button className="btn ghost sm" onClick={() => setLang(lang === "vi" ? "en" : "vi")} aria-label={t("shell.lang")}>
            <Icon name="globe" size={14} />
            {lang === "vi" ? "VI" : "EN"}
          </button>
        </div>

        <section className="card card-pad stack">
          <div>
            <h1>{t("me.title")}</h1>
            <p className="muted small" style={{ marginTop: 6 }}>
              {t("me.body")}
            </p>
          </div>
          {!key ? (
            <form className="row" onSubmit={submit}>
              <label htmlFor="me-key" className="sr-only">
                {t("me.key")}
              </label>
              <input id="me-key" className="input mono grow" type="password" autoComplete="off" placeholder="sk-clr-…" value={input} onChange={(e) => setInput(e.target.value)} autoFocus />
              <button className="btn primary" type="submit" disabled={!input.trim()}>
                {t("me.check")}
              </button>
            </form>
          ) : (
            <div className="row">
              <code className="grow ellipsis faint">{key.slice(0, 7)}…{key.slice(-4)}</code>
              <button className="btn sm" onClick={() => fetchInfo(key)} disabled={busy}>
                <Icon name="refresh" size={13} /> {t("me.refresh")}
              </button>
              <button className="btn sm ghost" onClick={forget}>
                {t("me.forget")}
              </button>
            </div>
          )}
          {err && (
            <div className="alert err" role="alert">
              <Icon name="alert" style={{ flex: "none", marginTop: 2 }} />
              <pre>{err}</pre>
            </div>
          )}
        </section>

        {info && <Usage info={info} />}

        <section className="card card-pad stack">
          <h2>{t("me.connect")}</h2>
          <ConnRow label={t("keys.msg.openai")} value={`${base}/v1`} />
          <ConnRow label={t("keys.msg.anthropic")} value={base} />
          <div>
            <h3 style={{ marginBottom: 6 }}>{t("me.errorsTitle")}</h3>
            <ul className="muted small" style={{ margin: 0, paddingLeft: 18, lineHeight: 1.8 }}>
              <li>{t("me.e401")}</li>
              <li>{t("me.e402")}</li>
              <li>{t("me.e403")}</li>
              <li>{t("me.e429")}</li>
            </ul>
          </div>
        </section>
      </div>
    </div>
  );
}

function ConnRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="row wrap">
      <span className="muted small" style={{ minWidth: 200 }}>
        {label}
      </span>
      <code className="grow ellipsis">{value}</code>
      <CopyButton text={value} className="btn xs" />
    </div>
  );
}

function Usage({ info }: { info: KeyInfo }) {
  const { t } = useI18n();
  const l = info.limits;
  const u = info.usage;
  const tokens = u.input_tokens + u.output_tokens;
  const p = t(`keys.per.${l.budget_period || "monthly"}` as Key);
  return (
    <section className="card card-pad stack">
      <div className="row wrap">
        <h2 className="mono">{info.name}</h2>
        <Badge tone={TONE[info.status] || "neutral"}>{t(`keys.st.${info.status}` as Key)}</Badge>
        <span className="spacer" />
        <span className="faint small">{info.expires_at ? t("me.expires", { d: toDateInput(info.expires_at) }) : t("me.noExpiry")}</span>
      </div>
      {info.note && <p className="muted small">{info.note}</p>}

      <div>
        <div className="row" style={{ marginBottom: 8 }}>
          <h3 className="grow">{t("me.period", { p })}</h3>
          <span className="faint small">
            {u.period_end ? t("me.resets", { d: toDateInput(u.period_end) }) : u.period_start ? t("me.since", { d: toDateInput(u.period_start) }) : ""}
          </span>
        </div>
        <div className="me-stats">
          <Stat label={t("me.cost")} value={fmtUsd(u.cost_usd)} sub={l.budget_usd ? `/ ${fmtUsd(l.budget_usd)}` : t("keys.noLimit")} />
          <Stat label={t("me.tokens")} value={fmtNum(tokens)} sub={l.budget_tokens ? `/ ${fmtNum(l.budget_tokens)}` : `${fmtNum(u.input_tokens)} / ${fmtNum(u.output_tokens)}`} />
          <Stat label={t("me.requests")} value={fmtNum(u.requests)} sub={`${t("keys.today")}: ${fmtNum(info.today.requests)} · ${fmtUsd(info.today.cost_usd)}`} />
        </div>
        {(l.budget_usd > 0 || l.budget_tokens > 0) && (
          <div style={{ marginTop: 12 }}>
            {l.budget_usd > 0 && <Meter pct={(u.cost_usd / l.budget_usd) * 100} value={`${fmtUsd(u.cost_usd)} / ${fmtUsd(l.budget_usd)}`} sub={p} label={t("keys.budgetUsd")} />}
            {l.budget_tokens > 0 && <Meter pct={(tokens / l.budget_tokens) * 100} value={`${fmtNum(tokens)} / ${fmtNum(l.budget_tokens)}`} sub={t("keys.tokensUnit")} label={t("keys.budgetTokens")} />}
          </div>
        )}
      </div>

      <div className="grid-2">
        <div>
          <h3 style={{ marginBottom: 6 }}>{t("keys.msg.models")}</h3>
          {info.models.length ? (
            <div className="row wrap" style={{ gap: 4 }}>
              {info.models.map((m) => (
                <span key={m} className="chip static">
                  {m}
                </span>
              ))}
            </div>
          ) : (
            <span className="muted small">{t("keys.allModels")}</span>
          )}
        </div>
        <div>
          <h3 style={{ marginBottom: 6 }}>{t("me.rate")}</h3>
          <span className="muted small">
            {[l.rpm ? t("keys.rpm", { n: l.rpm }) : "", l.tpm ? t("keys.tpm", { n: fmtNum(l.tpm) }) : "", l.max_parallel ? t("keys.parallel", { n: l.max_parallel }) : ""].filter(Boolean).join(" · ") || t("keys.noLimit")}
          </span>
        </div>
      </div>
    </section>
  );
}

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="me-stat">
      <div className="ms-label">{label}</div>
      <div className="ms-value">{value}</div>
      {sub && <div className="ms-sub">{sub}</div>}
    </div>
  );
}
