import { useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { fillTimeline, TimelineChart } from "../chart";
import { usePoll, useStored } from "../hooks";
import { useT } from "../i18n";
import { Icon, type IconName } from "../icons";
import type { Key } from "../dict";
import { useConfig, useStatus } from "../store";
import type { Bucket } from "../types";
import { Badge, Empty, ErrorNote } from "../ui";
import { fmtMs, fmtNum, fmtPct } from "../util";

const RANGES = ["1h", "6h", "24h", "7d", "30d"] as const;
type Range = (typeof RANGES)[number];

function Kpi({ label, value, unit, sub, icon, tone }: { label: string; value: string; unit?: string; sub?: ReactNode; icon: IconName; tone?: "err" | "warn" | "ok" }) {
  return (
    <div className={`kpi ${tone ? `tone-${tone}` : ""}`}>
      <div className="label">
        <span className="k-icon">
          <Icon name={icon} size={15} />
        </span>
        {label}
      </div>
      <div className="value" style={tone && tone !== "ok" ? { color: `var(--${tone})` } : undefined}>
        {value}
        {unit && <small>{unit}</small>}
      </div>
      <div className="sub">{sub ?? "\u00a0"}</div>
    </div>
  );
}

function MiniStat({ icon, label, value, tone }: { icon: IconName; label: string; value: ReactNode; tone?: "warn" | "err" }) {
  return (
    <div className="mini-stat">
      <Icon name={icon} size={16} />
      <div className="grow">
        <div className="ms-label">{label}</div>
        <div className="ms-value" style={tone ? { color: `var(--${tone})` } : undefined}>
          {value}
        </div>
      </div>
    </div>
  );
}

/** First-run checklist. Hidden once every step is done or the user dismisses it. */
function SetupGuide({ hasRequests }: { hasRequests: boolean }) {
  const t = useT();
  const { status } = useStatus();
  const { config } = useConfig();
  const [dismissed, setDismissed] = useStored<"0" | "1">("clr.setupDismissed", "0");
  const [visitedInt] = useStored<"0" | "1">("clr.visitedIntegrations", "0");
  if (!status || !config || dismissed === "1") return null;
  const steps: { done: boolean; to: string; title: Key; body: Key; cta: Key }[] = [
    { done: status.providers > 0, to: "/providers", title: "setup.s1", body: "setup.s1b", cta: "setup.s1c" },
    { done: config.api_keys.length > 0, to: "/settings", title: "setup.s2", body: "setup.s2b", cta: "setup.s2c" },
    { done: hasRequests, to: "/playground", title: "setup.s3", body: "setup.s3b", cta: "setup.s3c" },
    { done: visitedInt === "1", to: "/integrations", title: "setup.s4", body: "setup.s4b", cta: "setup.s4c" },
  ];
  const n = steps.filter((s) => s.done).length;
  if (n === steps.length) return null;
  return (
    <section className="card setup" aria-labelledby="setup-title">
      <div className="setup-head">
        <div className="grow">
          <h2 id="setup-title">{t("setup.title")}</h2>
          <p className="muted small" style={{ marginTop: 4 }}>
            {t("setup.body", { n, total: steps.length })}
          </p>
          <div className="setup-progress" aria-hidden="true">
            <i style={{ width: `${(n / steps.length) * 100}%` }} />
          </div>
        </div>
        <button className="btn ghost sm" onClick={() => setDismissed("1")}>
          {t("setup.dismiss")}
        </button>
      </div>
      <div className="steps">
        {steps.map((s, i) => (
          <Link key={s.to} to={s.to} className={`step ${s.done ? "done" : ""}`}>
            <span className="s-num">{s.done ? <Icon name="check" size={13} strokeWidth={3} /> : i + 1}</span>
            <span className="s-title">{t(s.title)}</span>
            <span className="s-body">{t(s.body)}</span>
            <span className="s-cta">
              {s.done ? t("setup.done") : t(s.cta)}
              {!s.done && <Icon name="right" size={13} />}
            </span>
          </Link>
        ))}
      </div>
    </section>
  );
}

export default function Overview() {
  const t = useT();
  const [range, setRange] = useStored<Range>("clr.range", "24h");
  const [showTable, setShowTable] = useState(false);
  const { status } = useStatus();
  const stats = usePoll((s) => api.stats(range, s), 15000, [range]);
  const st = stats.data;
  const tot = st?.total;

  const timeline = useMemo(() => (st ? fillTimeline(st.timeline, st.since, range, stats.updatedAt || Date.now()) : []), [st, range, stats.updatedAt]);

  const errRate = tot && tot.requests ? tot.errors / tot.requests : 0;
  const cachePct = tot && tot.input_tokens ? tot.cache_read_tokens / tot.input_tokens : 0;
  const m = status?.metrics;
  const hasRequests = (m?.total || 0) > 0 || (tot?.requests || 0) > 0;

  return (
    <div className="stack-lg">
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.overview")}</h1>
          <div className="desc">{t("ov.desc")}</div>
        </div>
        <div className="seg" role="group" aria-label={t("ov.range")}>
          {RANGES.map((r) => (
            <button key={r} aria-pressed={range === r} onClick={() => setRange(r)}>
              {r}
            </button>
          ))}
        </div>
      </div>

      <SetupGuide hasRequests={hasRequests} />

      {stats.error && !st && <ErrorNote error={stats.error} />}

      <div className="kpis" aria-busy={!st}>
        <Kpi icon="requests" label={t("ov.requests")} value={tot ? fmtNum(tot.requests) : "–"} sub={m ? t("ov.sinceStart", { n: fmtNum(m.total) }) : undefined} />
        <Kpi
          icon="alert"
          label={t("ov.errorRate")}
          value={tot ? fmtPct(errRate) : "–"}
          tone={errRate > 0.1 ? "err" : errRate > 0.02 ? "warn" : "ok"}
          sub={tot ? `${fmtNum(tot.errors)} ${t("ov.errorsLc")}` : undefined}
        />
        <Kpi icon="clock" label={t("ov.avgTtfb")} value={tot ? fmtMs(tot.avg_ttfb_ms) : "–"} sub={tot ? `p95 ${fmtMs(tot.p95_ttfb_ms)}${m && m.total > 0 ? ` · ${t("ov.live")} ${fmtMs(m.avg_ttfb_ms)}` : ""}` : undefined} />
        <Kpi
          icon="bolt"
          label={t("ov.tokens")}
          value={tot ? fmtNum(tot.input_tokens) : "–"}
          unit={tot ? `/ ${fmtNum(tot.output_tokens)}` : undefined}
          sub={tot ? `${t("ov.cacheRead")} ${fmtPct(cachePct)} · ${fmtNum(tot.cache_read_tokens)} tok` : undefined}
        />
      </div>

      <div className="mini-stats">
        <MiniStat icon="play" label={t("ov.inflight")} value={m ? m.inflight : "–"} />
        <MiniStat icon="providers" label={t("nav.providers")} value={status ? t("ov.providersAccounts", { p: status.providers, a: status.accounts }) : "–"} />
        <MiniStat
          icon="accounts"
          label={t("ov.cooling")}
          tone={status && status.accounts_cooling > 0 ? "warn" : undefined}
          value={
            status ? (
              <span className="row" style={{ gap: 8 }}>
                {status.accounts_cooling}
                <Link to="/accounts" className="small" style={{ fontWeight: 500 }}>
                  {t("ov.viewAccounts")}
                </Link>
              </span>
            ) : (
              "–"
            )
          }
        />
        {status && status.dropped_records > 0 && <MiniStat icon="alert" label={t("ov.droppedLabel")} tone="warn" value={fmtNum(status.dropped_records)} />}
      </div>

      {status && status.providers === 0 && (
        <div className="card">
          <Empty
            icon="providers"
            title={t("ov.noProvidersTitle")}
            body={t("ov.noProvidersBody")}
            action={
              <Link className="btn primary" to="/providers">
                <Icon name="plus" size={14} /> {t("prov.add")}
              </Link>
            }
          />
        </div>
      )}

      <div className="split">
        <div className="card">
          <div className="card-head">
            <h2 className="grow">{t("ov.reqTimeline")}</h2>
            <div className="legend" aria-hidden="true">
              <span>
                <i style={{ background: "var(--series-1)" }} />
                {t("ov.ok")}
              </span>
              <span>
                <i style={{ background: "var(--series-err)" }} />
                {t("ov.errors")}
              </span>
            </div>
            <button className="btn ghost sm" aria-pressed={showTable} onClick={() => setShowTable((v) => !v)}>
              {showTable ? t("ov.chart") : t("ov.table")}
            </button>
          </div>
          <div className="card-body">{showTable ? <TimelineTable data={timeline} /> : <TimelineChart data={timeline} range={range} mode="bars" label={t("ov.reqTimeline")} />}</div>
        </div>
        <div className="card">
          <div className="card-head">
            <h2 className="grow">{t("ov.ttfbTimeline")}</h2>
          </div>
          <div className="card-body">
            <TimelineChart data={timeline} range={range} mode="line" label={t("ov.ttfbTimeline")} />
          </div>
        </div>
      </div>

      <div className="split">
        <TopTable title={t("ov.topModels")} rows={st?.by_model} />
        <TopTable title={t("ov.topProviders")} rows={st?.by_provider} />
      </div>
      {st && st.by_key.length > 1 && <TopTable title={t("ov.topKeys")} rows={st.by_key} />}
    </div>
  );
}

function TimelineTable({ data }: { data: Bucket[] }) {
  const t = useT();
  const rows = data.filter((b) => b.requests > 0);
  return (
    <div className="table-wrap" style={{ maxHeight: 170, overflowY: "auto" }}>
      <table className="tbl">
        <thead>
          <tr>
            <th>{t("req.time")}</th>
            <th className="r">{t("ov.requests")}</th>
            <th className="r">{t("ov.errors")}</th>
            <th className="r">{t("ov.avgTtfb")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((b) => (
            <tr key={b.key}>
              <td className="nowrap">{new Date(Number(b.key)).toLocaleString()}</td>
              <td className="r">{fmtNum(b.requests)}</td>
              <td className="r">{fmtNum(b.errors)}</td>
              <td className="r">{fmtMs(b.avg_ttfb_ms)}</td>
            </tr>
          ))}
          {!rows.length && (
            <tr>
              <td colSpan={4} className="faint">
                {t("ov.noData")}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

function TopTable({ title, rows }: { title: string; rows?: Bucket[] }) {
  const t = useT();
  const max = Math.max(1, ...(rows || []).map((r) => r.requests));
  return (
    <div className="card">
      <div className="card-head">
        <h2>{title}</h2>
      </div>
      <div className="table-wrap" style={{ maxHeight: 360, overflowY: "auto" }}>
        <table className="tbl">
          <thead>
            <tr>
              <th>{t("common.name")}</th>
              <th className="r">{t("ov.requests")}</th>
              <th className="r">{t("ov.errors")}</th>
              <th className="r hide-sm">{t("ov.avgTtfb")}</th>
              <th className="r">{t("ov.tokInOut")}</th>
            </tr>
          </thead>
          <tbody>
            {!rows && (
              <tr>
                <td colSpan={5}>
                  <span className="skeleton">loading</span>
                </td>
              </tr>
            )}
            {rows && rows.length === 0 && (
              <tr>
                <td colSpan={5} className="faint">
                  {t("ov.noData")}
                </td>
              </tr>
            )}
            {rows?.slice(0, 12).map((r) => (
              <tr key={r.key}>
                <td className="bar-cell mono" style={{ maxWidth: 260 }}>
                  <div className="bar" style={{ width: `${(r.requests / max) * 100}%` }} />
                  <span className="ellipsis" style={{ display: "block" }} title={r.key}>
                    {r.key || "—"}
                  </span>
                </td>
                <td className="r">{fmtNum(r.requests)}</td>
                <td className="r">{r.errors > 0 ? <Badge tone="err" icon="alert">{fmtNum(r.errors)}</Badge> : <span className="faint">0</span>}</td>
                <td className="r hide-sm">{fmtMs(r.avg_ttfb_ms)}</td>
                <td className="r nowrap">
                  {fmtNum(r.input_tokens)} / {fmtNum(r.output_tokens)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
