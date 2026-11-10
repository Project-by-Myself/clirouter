import { useMemo, useState, type MouseEvent } from "react";
import { useWidth } from "./hooks";
import { useT } from "./i18n";
import type { Bucket } from "./types";
import { fmtMs, fmtNum } from "./util";

export const RANGE_STEP: Record<string, number> = {
  "1h": 5 * 60e3,
  "6h": 5 * 60e3,
  "24h": 3600e3,
  "7d": 86400e3,
  "30d": 86400e3,
};

export function fillTimeline(timeline: Bucket[], since: string, range: string, now: number): Bucket[] {
  const step = RANGE_STEP[range] || 3600e3;
  const byKey = new Map(timeline.map((b) => [Number(b.key), b]));
  const start = Math.floor(new Date(since).getTime() / step) * step;
  const out: Bucket[] = [];
  for (let k = start; k <= now; k += step) {
    out.push(byKey.get(k) || { key: String(k), requests: 0, errors: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, avg_ttfb_ms: 0, p95_ttfb_ms: 0 });
    if (out.length > 2000) break;
  }
  return out;
}

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / p;
  const m = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10;
  return m * p;
}

function tickLabel(ms: number, step: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  if (step >= 86400e3) return `${p(d.getDate())}/${p(d.getMonth() + 1)}`;
  return `${p(d.getHours())}:${p(d.getMinutes())}`;
}

interface Props {
  data: Bucket[];
  range: string;
  mode: "bars" | "line";
  height?: number;
  label: string;
}

export function TimelineChart({ data, range, mode, height = 170, label }: Props) {
  const t = useT();
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const step = RANGE_STEP[range] || 3600e3;
  const padL = 44;
  const padR = 8;
  const padT = 8;
  const padB = 22;
  const w = Math.max(width, 200);
  const iw = w - padL - padR;
  const ih = height - padT - padB;
  const n = data.length || 1;
  const bw = iw / n;

  const values = useMemo(() => data.map((b) => (mode === "bars" ? b.requests : b.requests > b.errors ? b.avg_ttfb_ms : 0)), [data, mode]);
  const max = niceMax(Math.max(0, ...values));
  const y = (v: number) => padT + ih - (v / max) * ih;
  const ticks = max >= 2 ? [0, max / 2, max] : [0, max];
  const empty = values.every((v) => !v);
  const fmtY = mode === "bars" ? fmtNum : (v: number) => (v === 0 ? "0" : fmtMs(v));
  const labelEvery = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(iw / 70))));

  const linePath = useMemo(() => {
    if (mode !== "line") return "";
    let d = "";
    let pen = false;
    data.forEach((_, i) => {
      const v = values[i];
      if (!v) {
        pen = false;
        return;
      }
      const x = padL + i * bw + bw / 2;
      d += `${pen ? "L" : "M"}${x.toFixed(1)},${y(v).toFixed(1)}`;
      pen = true;
    });
    return d;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, values, bw, max, mode]);

  const onMove = (e: MouseEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const x = e.clientX - r.left - padL;
    const i = Math.floor(x / bw);
    setHover(i >= 0 && i < data.length ? i : null);
  };

  const hb = hover != null ? data[hover] : null;
  const tipLeft = hover != null ? Math.min(Math.max(padL + hover * bw + bw / 2 - 80, 0), w - 170) : 0;
  const gap = bw > 6 ? 2 : bw > 3 ? 1 : 0;

  return (
    <div className="chart-wrap" ref={ref} style={{ height }}>
      {width > 0 && (
        <svg width={w} height={height} role="img" aria-label={label} onMouseMove={onMove} onMouseLeave={() => setHover(null)}>
          {ticks.map((tv, i) => (
            <g key={i}>
              <line x1={padL} x2={w - padR} y1={y(tv)} y2={y(tv)} stroke="var(--grid)" strokeDasharray={i === 0 ? undefined : "2 3"} />
              <text x={padL - 6} y={y(tv) + 4} textAnchor="end" fontSize="10.5" fill="var(--text-3)" className="num">
                {fmtY(tv)}
              </text>
            </g>
          ))}
          {data.map((b, i) =>
            i % labelEvery === 0 ? (
              <text key={b.key} x={padL + i * bw + bw / 2} y={height - 6} textAnchor="middle" fontSize="10.5" fill="var(--text-3)" className="num">
                {tickLabel(Number(b.key), step)}
              </text>
            ) : null,
          )}
          {hover != null && <rect x={padL + hover * bw} y={padT} width={bw} height={ih} fill="var(--text)" opacity={0.05} />}
          {mode === "bars" &&
            data.map((b, i) => {
              if (!b.requests) return null;
              const x = padL + i * bw + gap / 2;
              const bwi = Math.max(1, bw - gap);
              const ok = b.requests - b.errors;
              const yOk = y(ok);
              const yAll = y(b.requests);
              return (
                <g key={b.key}>
                  {ok > 0 && <rect x={x} y={yOk} width={bwi} height={padT + ih - yOk} fill="var(--series-1)" rx={bwi > 6 ? 2 : 0} />}
                  {b.errors > 0 && <rect x={x} y={yAll} width={bwi} height={Math.max(1, yOk - yAll - (ok > 0 ? 1 : 0))} fill="var(--series-err)" rx={bwi > 6 ? 2 : 0} />}
                </g>
              );
            })}
          {empty && (
            <text x={padL + iw / 2} y={padT + ih / 2} textAnchor="middle" fontSize="12" fill="var(--text-3)">
              {t("ov.noData")}
            </text>
          )}
          {mode === "line" && <path d={linePath} fill="none" stroke="var(--series-1)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />}
          {mode === "line" && hover != null && values[hover] > 0 && (
            <circle cx={padL + hover * bw + bw / 2} cy={y(values[hover])} r={4} fill="var(--series-1)" stroke="var(--surface)" strokeWidth={2} />
          )}
        </svg>
      )}
      {hb && (
        <div className="chart-tip" style={{ left: tipLeft, top: 0 }}>
          <div className="faint" style={{ marginBottom: 4 }}>
            {new Date(Number(hb.key)).toLocaleString()}
          </div>
          {mode === "bars" ? (
            <>
              <div className="row">
                <i style={{ width: 8, height: 8, borderRadius: 2, background: "var(--series-1)" }} />
                {t("ov.ok")}
                <span className="spacer" />
                <b>{fmtNum(hb.requests - hb.errors)}</b>
              </div>
              <div className="row">
                <i style={{ width: 8, height: 8, borderRadius: 2, background: "var(--series-err)" }} />
                {t("ov.errors")}
                <span className="spacer" />
                <b>{fmtNum(hb.errors)}</b>
              </div>
            </>
          ) : (
            <div className="row">
              {t("ov.avgTtfb")}
              <span className="spacer" />
              <b>{fmtMs(values[hover!])}</b>
            </div>
          )}
          <div className="row faint">
            {t("ov.tokens")}
            <span className="spacer" />
            {fmtNum(hb.input_tokens)} / {fmtNum(hb.output_tokens)}
          </div>
        </div>
      )}
    </div>
  );
}
