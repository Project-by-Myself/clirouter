import { useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { fillTimeline, TimelineChart } from "../chart";
import { usePoll, useStored } from "../hooks";
import { useT } from "../i18n";
import { Icon, type IconName } from "../icons";
import { useStatus } from "../store";
import type { Bucket } from "../types";
import { Badge, Empty, ErrorNote } from "../ui";
import { fmtMs, fmtNum, fmtPct } from "../util";

const RANGES = ["1h", "6h", "24h", "7d", "30d"] as const;
type Range = (typeof RANGES)[number];

function Kpi({ label, value, sub, icon, tone }: { label: string; value: string; sub?: ReactNode; icon: IconName; tone?: "err" | "warn" | "ok" }) {
  return (
    <div className="kpi">
      <div className="label">
        <Icon name={icon} size={13} />
        {label}
      </div>
      <div className="value" style={tone ? { color: `var(--${tone})` } : undefined}>
        {value}
      </div>
      <div className="sub">{sub ?? " "}</div>
    </div>
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

      {stats.error && !st && <ErrorNote error={stats.error} />}

      <div className="kpis" aria-busy={!st}>
        <Kpi icon="requests" label={t("ov.requests")} value={tot ? fmtNum(tot.requests) : "–"} sub={m ? t("ov.sinceStart", { n: fmtNum(m.total) }) : undefined} />
        <Kpi
          icon="alert"
          label={t("ov.errorRate")}
          value={tot ? fmtPct(errRate) : "–"}
          tone={errRate > 0.1 ? "err" : errRate > 0.02 ? "warn" : undefined}
          sub={tot ? `${fmtNum(tot.errors)} ${t("ov.errorsLc")}` : undefined}
        />
        <Kpi icon="clock" label={t("ov.avgTtfb")} value={tot ? fmtMs(tot.avg_ttfb_ms) : "–"} sub={m ? `${t("ov.live")}: ${fmtMs(m.avg_ttfb_ms)}` : undefined} />
        <Kpi icon="clock" label={t("ov.p95Ttfb")} value={tot ? fmtMs(tot.p95_ttfb_ms) : "–"} />
        <Kpi icon="down" label={t("ov.tokIn")} value={tot ? fmtNum(tot.input_tokens) : "–"} />
        <Kpi icon="up" label={t("ov.tokOut")} value={tot ? fmtNum(tot.output_tokens) : "–"} />
        <Kpi icon="bolt" label={t("ov.cacheRead")} value={tot ? fmtPct(cachePct) : "–"} sub={tot ? `${fmtNum(tot.cache_read_tokens)} tok` : undefined} />
        <Kpi icon="play" label={t("ov.inflight")} value={m ? String(m.inflight) : "–"} sub={status ? t("ov.providersAccounts", { p: status.providers, a: status.accounts }) : undefined} />
        <Kpi
          icon="accounts"
          label={t("ov.cooling")}
          value={status ? String(status.accounts_cooling) : "–"}
          tone={status && status.accounts_cooling > 0 ? "warn" : undefined}
          sub={status && status.dropped_records > 0 ? t("ov.dropped", { n: status.dropped_records }) : status ? <Link to="/accounts">{t("ov.viewAccounts")}</Link> : undefined}
        />
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
