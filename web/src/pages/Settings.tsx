import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { usePoll } from "../hooks";
import { setPassword } from "../api";
import type { Key } from "../dict";
import { useT, type TFn } from "../i18n";
import { Icon } from "../icons";
import { diffConfig, useConfig } from "../store";
import type { Config, Logging, Price, Routing, Timeouts } from "../types";
import { Badge, ErrorNote, Field, Switch, useConfirm } from "../ui";
import { clone, fmtDuration, isMasked, maskHint, parseDuration } from "../util";

const TIMEOUTS: (keyof Timeouts)[] = ["connect", "first_byte", "idle", "total", "early_ping", "keepalive"];

interface PriceRow {
  model: string;
  input: string;
  output: string;
  cache_read: string;
  cache_write: string;
}

const PRICE_FIELDS = ["input", "output", "cache_read", "cache_write"] as const;

function toPriceRow(model: string, p: Price): PriceRow {
  const f = (v?: number) => (v === undefined || v === null ? "" : String(v));
  return { model, input: f(p.input), output: f(p.output), cache_read: f(p.cache_read), cache_write: f(p.cache_write) };
}

const priceBad = (v: string) => v.trim() !== "" && !(Number.isFinite(Number(v)) && Number(v) >= 0);

interface Draft {
  listen: string;
  public_url: string;
  pricing: PriceRow[];
  admin_password: string;
  timeouts: Record<keyof Timeouts, string>;
  routing: { strategy: string; max_attempts: string; session_affinity: boolean; max_body_mb: string };
  logging: { level: string; retention_days: string; capture_bodies: boolean };
}

function toDraft(c: Config): Draft {
  const to = {} as Record<keyof Timeouts, string>;
  for (const k of TIMEOUTS) to[k] = fmtDuration(c.timeouts[k]) || "0s";
  return {
    listen: c.listen,
    public_url: c.public_url || "",
    pricing: Object.entries(c.pricing || {}).map(([m, p]) => toPriceRow(m, p)),
    admin_password: c.admin_password || "",
    timeouts: to,
    routing: { strategy: c.routing.strategy, max_attempts: String(c.routing.max_attempts), session_affinity: c.routing.session_affinity, max_body_mb: String(c.routing.max_body_mb) },
    logging: { level: c.logging.level || "info", retention_days: String(c.logging.retention_days), capture_bodies: c.logging.capture_bodies },
  };
}

const intOk = (s: string) => /^\d+$/.test(s.trim());

function validate(d: Draft, t: TFn): Record<string, string> {
  const e: Record<string, string> = {};
  if (!d.listen.trim()) e.listen = t("val.required");
  else if (!/:\d+$/.test(d.listen.trim())) e.listen = t("set.listenFormat");
  for (const k of TIMEOUTS) if (parseDuration(d.timeouts[k]) === null) e[`t.${k}`] = t("val.duration");
  if (!intOk(d.routing.max_attempts)) e.max_attempts = t("val.int");
  if (!intOk(d.routing.max_body_mb)) e.max_body_mb = t("val.int");
  if (!intOk(d.logging.retention_days)) e.retention_days = t("val.int");
  if (d.public_url.trim() && !/^https?:\/\/[^\s/]+/.test(d.public_url.trim())) e.public_url = t("val.url");
  const seen = new Set<string>();
  for (const r of d.pricing) {
    const m = r.model.trim();
    if (!m || seen.has(m) || PRICE_FIELDS.some((f) => priceBad(r[f])) || r.input.trim() === "" || r.output.trim() === "") {
      e.pricing = t("set.priceErr");
      break;
    }
    seen.add(m);
  }
  return e;
}

function apply(c: Config, d: Draft): Config {
  const n = clone(c);
  n.listen = d.listen.trim();
  n.public_url = d.public_url.trim().replace(/\/+$/, "");
  const pr: Record<string, Price> = {};
  for (const r of d.pricing) {
    const m = r.model.trim();
    if (!m) continue;
    const p: Price = { input: Number(r.input) || 0, output: Number(r.output) || 0 };
    if (r.cache_read.trim() !== "") p.cache_read = Number(r.cache_read) || 0;
    if (r.cache_write.trim() !== "") p.cache_write = Number(r.cache_write) || 0;
    pr[m] = p;
  }
  n.pricing = pr;
  n.admin_password = d.admin_password;
  for (const k of TIMEOUTS) n.timeouts[k] = parseDuration(d.timeouts[k]) ?? c.timeouts[k];
  const r: Routing = { strategy: d.routing.strategy, max_attempts: parseInt(d.routing.max_attempts, 10) || 0, session_affinity: d.routing.session_affinity, max_body_mb: parseInt(d.routing.max_body_mb, 10) || 0 };
  const l: Logging = { level: d.logging.level, retention_days: parseInt(d.logging.retention_days, 10) || 0, capture_bodies: d.logging.capture_bodies };
  n.routing = r;
  n.logging = l;
  return n;
}

export default function Settings() {
  const t = useT();
  const confirm = useConfirm();
  const { config, error, save } = useConfig();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [busy, setBusy] = useState(false);
  const [serverErr, setServerErr] = useState<string>();
  const [newPw, setNewPw] = useState("");

  useEffect(() => {
    if (config && !draft) setDraft(toDraft(config));
  }, [config, draft]);

  if (!config || !draft) return error ? <ErrorNote error={error} /> : <div className="faint">{t("common.loading")}</div>;

  const next = apply(config, draft);
  const changes = diffConfig(config, next, t);
  const errs = validate(draft, t);
  const nErr = Object.keys(errs).length;
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft({ ...draft, [k]: v });

  const submit = async () => {
    if (nErr) return;
    if (config.listen !== next.listen && !(await confirm({ title: t("set.listenConfirm"), body: t("set.listenNote"), confirm: t("common.save") }))) return;
    if (config.admin_password && !next.admin_password && !(await confirm({ title: t("set.pwRemoveTitle"), body: t("set.pwRemoveBody"), confirm: t("common.save"), danger: true }))) return;
    setBusy(true);
    setServerErr(undefined);
    const r = await save(next);
    setBusy(false);
    if (r.ok) {
      const pw = next.admin_password || "";
      if (!isMasked(pw) && !pw.startsWith("env:")) setPassword(pw);
      setNewPw("");
      setDraft(null);
    } else setServerErr(r.error);
  };

  const timeoutHelp: Record<keyof Timeouts, Key> = {
    connect: "set.t.connect",
    first_byte: "set.t.first_byte",
    idle: "set.t.idle",
    total: "set.t.total",
    early_ping: "set.t.early_ping",
    keepalive: "set.t.keepalive",
  };

  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.settings")}</h1>
          <div className="desc">{t("set.desc")}</div>
        </div>
      </div>
      <div className="stack-lg">
        <section className="card">
          <div className="card-head">
            <h2>{t("set.server")}</h2>
          </div>
          <div className="card-body grid-2">
            <Field label="listen" hint={t("set.listenHint")} error={errs.listen}>
              {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!errs.listen} className="input mono" value={draft.listen} onChange={(e) => set("listen", e.target.value)} />}
            </Field>
            <div className="field">
              <span className="field-label">data_dir</span>
              <div className="input mono" style={{ display: "flex", alignItems: "center", background: "var(--surface-2)" }} aria-readonly="true">
                <span className="ellipsis">{config.data_dir || "–"}</span>
              </div>
              <span className="hint">{t("set.dataDirHint")}</span>
            </div>
            <div style={{ gridColumn: "1 / -1" }}>
              <Field label={t("set.publicUrl")} hint={t("set.publicUrlHint")} error={errs.public_url}>
                {(id, d) => (
                  <input id={id} aria-describedby={d} aria-invalid={!!errs.public_url} className="input mono" value={draft.public_url} placeholder="http://100.x.y.z:8317" onChange={(e) => set("public_url", e.target.value)} />
                )}
              </Field>
            </div>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2 className="grow">{t("set.clientKeys")}</h2>
            <Badge>{t("set.keysCount", { n: config.api_keys.length })}</Badge>
          </div>
          <div className="card-body row wrap">
            <p className="muted small grow" style={{ minWidth: 240 }}>
              {t("set.keysMoved")}
            </p>
            <Link className="btn sm" to="/keys">
              <Icon name="key" size={13} /> {t("set.keysOpen")}
            </Link>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>{t("set.adminPw")}</h2>
          </div>
          <div className="card-body stack">
            <p className="muted small">{t("set.adminPwHint")}</p>
            <div className="row wrap">
              {draft.admin_password ? (
                <>
                  <Badge tone="ok" icon="lock">
                    {isMasked(draft.admin_password) ? t("set.pwSet", { hint: maskHint(draft.admin_password) }) : draft.admin_password.startsWith("env:") ? draft.admin_password : t("set.pwNew")}
                  </Badge>
                  <button className="btn sm danger" onClick={() => set("admin_password", "")}>
                    {t("set.pwRemove")}
                  </button>
                </>
              ) : (
                <Badge tone="warn" icon="alert">
                  {t("set.pwNone")}
                </Badge>
              )}
            </div>
            <div className="row wrap">
              <input className="input mono" type="password" autoComplete="new-password" style={{ maxWidth: 280 }} aria-label={t("set.pwNewLabel")} placeholder={t("set.pwNewLabel")} value={newPw} onChange={(e) => setNewPw(e.target.value)} />
              <button className="btn sm" disabled={!newPw} onClick={() => set("admin_password", newPw)}>
                {t("set.pwApply")}
              </button>
            </div>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>{t("set.timeouts")}</h2>
          </div>
          <div className="card-body grid-3">
            {TIMEOUTS.map((k) => (
              <Field key={k} label={k} hint={t(timeoutHelp[k])} error={errs[`t.${k}`]}>
                {(id, d) => (
                  <input id={id} aria-describedby={d} aria-invalid={!!errs[`t.${k}`]} className="input mono" value={draft.timeouts[k]} onChange={(e) => set("timeouts", { ...draft.timeouts, [k]: e.target.value })} placeholder="90s" />
                )}
              </Field>
            ))}
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>{t("set.routing")}</h2>
          </div>
          <div className="card-body grid-2">
            <Field label="strategy" hint={t(`set.strat.${draft.routing.strategy}` as Key) || ""}>
              {(id, d) => (
                <select id={id} aria-describedby={d} className="input" value={draft.routing.strategy} onChange={(e) => set("routing", { ...draft.routing, strategy: e.target.value })}>
                  <option value="round-robin">round-robin</option>
                  <option value="fill-first">fill-first</option>
                  <option value="least-busy">least-busy</option>
                </select>
              )}
            </Field>
            <Field label="max_attempts" hint={t("set.maxAttemptsHint")} error={errs.max_attempts}>
              {(id, d) => <input id={id} aria-describedby={d} className="input num" inputMode="numeric" value={draft.routing.max_attempts} onChange={(e) => set("routing", { ...draft.routing, max_attempts: e.target.value })} />}
            </Field>
            <Field label="max_body_mb" hint={t("set.maxBodyHint")} error={errs.max_body_mb}>
              {(id, d) => <input id={id} aria-describedby={d} className="input num" inputMode="numeric" value={draft.routing.max_body_mb} onChange={(e) => set("routing", { ...draft.routing, max_body_mb: e.target.value })} />}
            </Field>
            <div className="field" style={{ justifyContent: "center" }}>
              <Switch checked={draft.routing.session_affinity} onChange={(v) => set("routing", { ...draft.routing, session_affinity: v })} label="session_affinity" />
              <span className="hint">{t("set.affinityHint")}</span>
            </div>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>{t("set.logging")}</h2>
          </div>
          <div className="card-body grid-3">
            <Field label="level">
              {(id) => (
                <select id={id} className="input" value={draft.logging.level} onChange={(e) => set("logging", { ...draft.logging, level: e.target.value })}>
                  {["debug", "info", "warn", "error"].map((l) => (
                    <option key={l} value={l}>
                      {l}
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <Field label="retention_days" hint={t("set.retentionHint")} error={errs.retention_days}>
              {(id, d) => <input id={id} aria-describedby={d} className="input num" inputMode="numeric" value={draft.logging.retention_days} onChange={(e) => set("logging", { ...draft.logging, retention_days: e.target.value })} />}
            </Field>
            <div className="field" style={{ justifyContent: "center" }}>
              <Switch checked={draft.logging.capture_bodies} onChange={(v) => set("logging", { ...draft.logging, capture_bodies: v })} label="capture_bodies" />
              <span className="hint">{t("set.captureHint")}</span>
            </div>
          </div>
        </section>

        <PricingCard rows={draft.pricing} onChange={(r) => set("pricing", r)} error={errs.pricing} />
      </div>

      {(changes.length > 0 || serverErr) && (
        <div className="savebar" role="region" aria-label={t("save.unsaved")}>
          <div className="grow" style={{ minWidth: 200 }}>
            <div style={{ fontWeight: 600 }}>{t("save.unsavedN", { n: changes.length })}</div>
            <div className="faint small mono ellipsis" title={changes.join("\n")}>
              {changes.slice(0, 4).join(" · ")}
            </div>
            {nErr > 0 && (
              <div className="small" style={{ color: "var(--err)" }}>
                <Icon name="alert" size={12} /> {t("val.fixErrors", { n: nErr })}
              </div>
            )}
            {serverErr && (
              <div className="small" style={{ color: "var(--err)", whiteSpace: "pre-wrap" }}>
                <Icon name="alert" size={12} /> {serverErr}
              </div>
            )}
          </div>
          <button
            className="btn"
            disabled={busy}
            onClick={() => {
              setDraft(toDraft(config));
              setServerErr(undefined);
              setNewPw("");
            }}
          >
            {t("save.discard")}
          </button>
          <button className="btn primary" onClick={submit} disabled={busy || nErr > 0}>
            {busy ? t("common.saving") : t("common.save")}
          </button>
        </div>
      )}
    </div>
  );
}

function PricingCard({ rows, onChange, error }: { rows: PriceRow[]; onChange: (r: PriceRow[]) => void; error?: string }) {
  const t = useT();
  const info = usePoll((sig) => api.pricing(sig), 0);
  const [q, setQ] = useState("");
  const builtin = useMemo(() => {
    const b = Object.entries(info.data?.builtin || {}).sort(([a], [b2]) => a.localeCompare(b2));
    const f = q.trim().toLowerCase();
    return f ? b.filter(([m]) => m.toLowerCase().includes(f)) : b;
  }, [info.data, q]);
  const upd = (i: number, k: keyof PriceRow, v: string) => onChange(rows.map((r, j) => (j === i ? { ...r, [k]: v } : r)));
  const override = (m: string, p: Price) => {
    if (rows.some((r) => r.model === m)) return;
    onChange([...rows, toPriceRow(m, p)]);
  };
  const fmtP = (v?: number) => (v === undefined ? "–" : `$${v}`);
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="grow">{t("set.pricing")}</h2>
        <button className="btn sm" onClick={() => onChange([...rows, { model: "", input: "", output: "", cache_read: "", cache_write: "" }])}>
          <Icon name="plus" size={13} /> {t("set.addPrice")}
        </button>
      </div>
      <div className="card-body stack">
        <p className="muted small">{t("set.pricingHint")}</p>
        {error && <ErrorNote error={error} />}
        {rows.length === 0 ? (
          <p className="faint small">{t("set.noOverrides")}</p>
        ) : (
          <div className="table-wrap">
            <table className="tbl price-tbl">
              <thead>
                <tr>
                  <th>{t("set.pricingModel")}</th>
                  <th className="r">{t("set.priceIn")}</th>
                  <th className="r">{t("set.priceOut")}</th>
                  <th className="r">{t("set.priceCacheR")}</th>
                  <th className="r">{t("set.priceCacheW")}</th>
                  <th>
                    <span className="sr-only">{t("common.actions")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r, i) => (
                  <tr key={i}>
                    <td style={{ minWidth: 200 }}>
                      <input className="input mono" aria-label={`${t("set.pricingModel")} ${i + 1}`} value={r.model} placeholder="provider/model" onChange={(e) => upd(i, "model", e.target.value)} />
                    </td>
                    {PRICE_FIELDS.map((f) => (
                      <td key={f} style={{ width: 110 }}>
                        <input className="input num" inputMode="decimal" aria-label={`${r.model || i + 1} ${f}`} aria-invalid={priceBad(r[f])} value={r[f]} placeholder={f.startsWith("cache") ? "–" : "0"} onChange={(e) => upd(i, f, e.target.value)} />
                      </td>
                    ))}
                    <td className="r">
                      <button className="btn ghost icon sm" aria-label={t("common.removeX", { x: r.model || String(i + 1) })} onClick={() => onChange(rows.filter((_, j) => j !== i))}>
                        <Icon name="trash" size={14} />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {info.data && (
          <details className="builtin">
            <summary>{t("set.builtin", { n: Object.keys(info.data.builtin).length })}</summary>
            <input className="input" style={{ maxWidth: 260, marginBottom: 8 }} type="search" aria-label={t("set.filterPrices")} placeholder={t("set.filterPrices")} value={q} onChange={(e) => setQ(e.target.value)} />
            <div className="table-wrap" style={{ maxHeight: 320, overflowY: "auto" }}>
              <table className="tbl">
                <thead>
                  <tr>
                    <th>{t("set.pricingModel")}</th>
                    <th className="r">{t("set.priceIn")}</th>
                    <th className="r">{t("set.priceOut")}</th>
                    <th className="r">{t("set.priceCacheR")}</th>
                    <th className="r">{t("set.priceCacheW")}</th>
                    <th>
                      <span className="sr-only">{t("common.actions")}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {builtin.map(([m, p]) => (
                    <tr key={m}>
                      <td className="mono">{m}</td>
                      <td className="r">{fmtP(p.input)}</td>
                      <td className="r">{fmtP(p.output)}</td>
                      <td className="r">{fmtP(p.cache_read)}</td>
                      <td className="r">{fmtP(p.cache_write)}</td>
                      <td className="r">
                        <button className="btn xs" disabled={rows.some((r) => r.model === m)} onClick={() => override(m, p)}>
                          {t("set.override")}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        )}
      </div>
    </section>
  );
}
