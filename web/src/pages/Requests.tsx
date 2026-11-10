import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api } from "../api";
import { usePoll, useStored } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { useConfig } from "../store";
import type { RecordT } from "../types";
import { Badge, Drawer, Empty, ErrorNote, StatusCode } from "../ui";
import { fmtDateTime, fmtMs, fmtNum, fmtTime, isErrStatus } from "../util";

type Tab = "live" | "history";
const rkey = (r: RecordT) => `${r.ts}|${r.id}`;

export default function Requests() {
  const t = useT();
  const [tab, setTab] = useStored<Tab>("clr.reqTab", "live");
  const [sel, setSel] = useState<RecordT | null>(null);
  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.requests")}</h1>
          <div className="desc">{t("req.desc")}</div>
        </div>
      </div>
      <div className="tabs" role="tablist">
        <button role="tab" aria-selected={tab === "live"} onClick={() => setTab("live")}>
          {t("req.live")}
        </button>
        <button role="tab" aria-selected={tab === "history"} onClick={() => setTab("history")}>
          {t("req.history")}
        </button>
      </div>
      {tab === "live" ? <Live onSelect={setSel} selected={sel} /> : <History onSelect={setSel} selected={sel} />}
      {sel && <Detail r={sel} onClose={() => setSel(null)} />}
    </div>
  );
}

function Live({ onSelect, selected }: { onSelect: (r: RecordT) => void; selected: RecordT | null }) {
  const t = useT();
  const [paused, setPaused] = useState(false);
  const { data, error, updatedAt } = usePoll((s) => api.live(100, s), 2000, [], !paused);
  return (
    <div className="card">
      <div className="card-head">
        <span className={`dot ${paused ? "warn" : "ok"}`} aria-hidden="true" />
        <span className="small">{paused ? t("req.paused") : t("req.tailing")}</span>
        <span className="faint small hide-sm">{updatedAt ? `· ${fmtTime(updatedAt)}` : ""}</span>
        <span className="spacer" />
        <button className="btn sm" onClick={() => setPaused((p) => !p)} aria-pressed={paused}>
          <Icon name={paused ? "play" : "pause"} size={13} /> {paused ? t("req.resume") : t("req.pause")}
        </button>
      </div>
      {error && (
        <div className="card-body">
          <ErrorNote error={error} />
        </div>
      )}
      <RecordTable rows={data} onSelect={onSelect} selected={selected} empty={t("req.liveEmpty")} />
    </div>
  );
}

function History({ onSelect, selected }: { onSelect: (r: RecordT) => void; selected: RecordT | null }) {
  const t = useT();
  const { config, models } = useConfig();
  const [provider, setProvider] = useState("");
  const [model, setModel] = useState("");
  const [modelQ, setModelQ] = useState("");
  const [errors, setErrors] = useState(false);
  const [rows, setRows] = useState<RecordT[]>();
  const [err, setErr] = useState<Error>();
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const gen = useRef(0);
  const LIMIT = 100;

  useEffect(() => {
    const id = window.setTimeout(() => setModel(modelQ.trim()), 350);
    return () => window.clearTimeout(id);
  }, [modelQ]);

  const load = useCallback(
    async (before?: number) => {
      const g = before ? gen.current : ++gen.current;
      setBusy(true);
      try {
        const r = await api.requests({ limit: LIMIT, before, provider, model, errors });
        if (g !== gen.current) return;
        setRows((prev) => (before ? [...(prev || []), ...r] : r));
        setDone(r.length < LIMIT);
        setErr(undefined);
      } catch (e) {
        if (g === gen.current) setErr(e as Error);
      } finally {
        if (g === gen.current) setBusy(false);
      }
    },
    [provider, model, errors],
  );

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="card">
      <div className="card-head" style={{ flexWrap: "wrap", gap: 8 }}>
        <select className="input" style={{ width: "auto", minWidth: 140 }} aria-label={t("req.provider")} value={provider} onChange={(e) => setProvider(e.target.value)}>
          <option value="">{t("req.allProviders")}</option>
          {config?.providers.map((p) => (
            <option key={p.id} value={p.id}>
              {p.id}
            </option>
          ))}
        </select>
        <input className="input mono" style={{ width: 220 }} list="req-models" type="search" aria-label={t("req.model")} placeholder={t("req.modelPh")} value={modelQ} onChange={(e) => setModelQ(e.target.value)} />
        <datalist id="req-models">
          {models.map((m) => (
            <option key={m.id} value={m.id} />
          ))}
        </datalist>
        <label className="check small">
          <input type="checkbox" checked={errors} onChange={(e) => setErrors(e.target.checked)} />
          {t("req.errorsOnly")}
        </label>
        <span className="spacer" />
        <button className="btn sm" onClick={() => load()} disabled={busy} aria-label={t("common.refresh")}>
          <Icon name="refresh" size={13} /> <span className="hide-sm">{t("common.refresh")}</span>
        </button>
      </div>
      {err && (
        <div className="card-body">
          <ErrorNote error={err} />
        </div>
      )}
      <RecordTable rows={rows} onSelect={onSelect} selected={selected} empty={t("req.historyEmpty")} />
      {rows && rows.length > 0 && (
        <div className="card-body row" style={{ justifyContent: "center", borderTop: "1px solid var(--border)" }}>
          {done ? (
            <span className="faint small">{t("req.end", { n: rows.length })}</span>
          ) : (
            <button className="btn" onClick={() => load(rows[rows.length - 1].id)} disabled={busy}>
              {busy ? t("common.loading") : t("req.loadMore")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function RecordTable({ rows, onSelect, selected, empty }: { rows?: RecordT[]; onSelect: (r: RecordT) => void; selected: RecordT | null; empty: string }) {
  const t = useT();
  if (!rows) return <div className="card-body faint">{t("common.loading")}</div>;
  if (rows.length === 0) return <Empty icon="requests" title={empty} body={t("req.emptyBody")} />;
  const selKey = selected ? rkey(selected) : "";
  return (
    <div className="table-wrap">
      <table className="tbl">
        <thead>
          <tr>
            <th>{t("req.time")}</th>
            <th className="hide-sm">{t("req.client")}</th>
            <th>{t("req.route")}</th>
            <th>{t("req.status")}</th>
            <th className="r">TTFB</th>
            <th className="r hide-sm">{t("req.duration")}</th>
            <th className="r">{t("req.tokens")}</th>
            <th className="r hide-sm" title={t("req.attempts")}>
              #
            </th>
            <th className="hide-sm">
              <span className="sr-only">{t("req.passthrough")}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const k = rkey(r);
            return (
              <tr
                key={k}
                className={`clickable ${k === selKey ? "selected" : ""} ${isErrStatus(r.status) ? "err-row" : ""}`}
                tabIndex={0}
                onClick={() => onSelect(r)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    onSelect(r);
                  }
                }}
                aria-label={`${fmtTime(r.ts)} ${r.model} ${r.status}`}
              >
                <td className="nowrap num" title={fmtDateTime(r.ts)}>
                  {fmtTime(r.ts)}
                </td>
                <td className="hide-sm nowrap">
                  <div className="ellipsis" style={{ maxWidth: 120 }}>
                    {r.client_key || "–"}
                  </div>
                  <div className="faint small">{r.client_format}</div>
                </td>
                <td style={{ maxWidth: 320 }}>
                  <div className="mono ellipsis" title={r.model}>
                    {r.model || "–"}
                  </div>
                  <div className="faint small mono ellipsis" title={`${r.provider}/${r.upstream_model}`}>
                    → {r.provider ? `${r.provider}/${r.upstream_model || r.model}` : "–"}
                  </div>
                </td>
                <td>
                  <StatusCode status={r.status} />
                </td>
                <td className="r nowrap">{fmtMs(r.ttfb_ms)}</td>
                <td className="r nowrap hide-sm">{fmtMs(r.duration_ms)}</td>
                <td className="r nowrap num small">
                  {fmtNum(r.input_tokens)} / {fmtNum(r.output_tokens)}
                  {r.cache_read_tokens > 0 && <div className="faint">cache {fmtNum(r.cache_read_tokens)}</div>}
                </td>
                <td className="r hide-sm">{r.attempts > 1 ? <Badge tone="warn">{r.attempts}</Badge> : <span className="faint">{r.attempts || 1}</span>}</td>
                <td className="hide-sm">
                  {r.passthrough && (
                    <span title={t("req.passthrough")} style={{ color: "var(--accent)" }}>
                      <Icon name="bolt" size={13} aria-label={t("req.passthrough")} aria-hidden={false} role="img" />
                    </span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function Detail({ r, onClose }: { r: RecordT; onClose: () => void }) {
  const t = useT();
  const rows: [string, ReactNode][] = [
    ["ID", r.id < 0 ? `${r.id} (${t("req.memory")})` : r.id],
    [t("req.time"), fmtDateTime(r.ts)],
    [t("req.clientKey"), r.client_key || "–"],
    [t("req.clientFormat"), r.client_format || "–"],
    [t("req.model"), <span className="mono">{r.model || "–"}</span>],
    [t("req.provider"), <span className="mono">{r.provider || "–"}</span>],
    [t("req.account"), <span className="mono">{r.account || "–"}</span>],
    [t("req.upstreamModel"), <span className="mono">{r.upstream_model || "–"}</span>],
    [t("req.status"), <StatusCode status={r.status} />],
    ["Stream", r.stream ? t("common.yes") : t("common.no")],
    [t("req.passthrough"), r.passthrough ? t("common.yes") : t("common.no")],
    [t("req.attempts"), r.attempts],
    ["TTFB", fmtMs(r.ttfb_ms)],
    [t("req.duration"), fmtMs(r.duration_ms)],
    [t("req.inTok"), fmtNum(r.input_tokens)],
    [t("req.outTok"), fmtNum(r.output_tokens)],
    ["Cache read", fmtNum(r.cache_read_tokens)],
    ["Cache write", fmtNum(r.cache_write_tokens)],
    ["Reasoning", fmtNum(r.reasoning_tokens)],
  ];
  return (
    <Drawer open onClose={onClose} title={t("req.detail")} width={520}>
      <div className="stack">
        {r.error && (
          <div className="alert err" role="alert">
            <Icon name="alert" style={{ flex: "none", marginTop: 2 }} />
            <pre>{r.error}</pre>
          </div>
        )}
        <dl className="detail-grid">
          {rows.map(([k, v]) => (
            <div key={k} style={{ display: "contents" }}>
              <dt>{k}</dt>
              <dd className="num">{v}</dd>
            </div>
          ))}
        </dl>
      </div>
    </Drawer>
  );
}
