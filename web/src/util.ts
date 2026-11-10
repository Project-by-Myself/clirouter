const NS = { ns: 1, us: 1e3, "µs": 1e3, ms: 1e6, s: 1e9, m: 60e9, h: 3600e9 } as const;

export function fmtDuration(ns: number): string {
  if (!ns) return "";
  let rest = Math.round(ns);
  if (rest % 1e6 !== 0) {
    if (rest < 1e6) return rest % 1e3 === 0 ? `${rest / 1e3}µs` : `${rest}ns`;
    return `${+(rest / 1e6).toFixed(3)}ms`;
  }
  if (rest < 1e9) return `${rest / 1e6}ms`;
  let out = "";
  const h = Math.floor(rest / 3600e9);
  rest -= h * 3600e9;
  const m = Math.floor(rest / 60e9);
  rest -= m * 60e9;
  if (h) out += `${h}h`;
  if (m) out += `${m}m`;
  if (rest) out += `${+(rest / 1e9).toFixed(3)}s`;
  return out || "0s";
}

export function parseDuration(s: string): number | null {
  const v = s.trim();
  if (v === "" || v === "0") return 0;
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
  let total = 0;
  let consumed = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(v))) {
    if (m.index !== consumed) return null;
    total += parseFloat(m[1]) * NS[m[2] as keyof typeof NS];
    consumed = m.index + m[0].length;
  }
  if (consumed !== v.length) return null;
  return Math.round(total);
}

export function fmtNum(n: number | undefined | null): string {
  if (n == null || !isFinite(n)) return "–";
  const a = Math.abs(n);
  if (a >= 1e9) return `${+(n / 1e9).toFixed(2)}B`;
  if (a >= 1e6) return `${+(n / 1e6).toFixed(2)}M`;
  if (a >= 1e4) return `${+(n / 1e3).toFixed(1)}k`;
  return Math.round(n).toLocaleString("en-US");
}

export function fmtMs(ms: number | undefined | null): string {
  if (ms == null || !isFinite(ms) || ms <= 0) return "–";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(ms < 10000 ? 2 : 1)}s`;
  return `${Math.floor(ms / 60000)}m${Math.round((ms % 60000) / 1000)}s`;
}

export function fmtPct(x: number): string {
  if (!isFinite(x)) return "–";
  return `${(x * 100).toFixed(x < 0.1 && x > 0 ? 2 : 1)}%`;
}

export function fmtUptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m ${s % 60}s`;
}

export function fmtTime(ts: string | number | Date): string {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "–";
  return d.toLocaleTimeString("en-GB", { hour12: false });
}

export function fmtDateTime(ts: string | number | Date): string {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "–";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export function fmtCountdown(ms: number): string {
  if (ms <= 0) return "0s";
  const s = Math.ceil(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const r = s % 60;
  if (h) return `${h}h${String(m).padStart(2, "0")}m`;
  if (m) return `${m}m${String(r).padStart(2, "0")}s`;
  return `${r}s`;
}

export function fmtAgo(ts: string | undefined, now: number): string {
  if (!ts) return "–";
  const d = now - new Date(ts).getTime();
  if (d < 0) return fmtTime(ts);
  if (d < 60e3) return `${Math.max(1, Math.round(d / 1e3))}s`;
  if (d < 3600e3) return `${Math.round(d / 60e3)}m`;
  if (d < 86400e3) return `${Math.round(d / 3600e3)}h`;
  return `${Math.round(d / 86400e3)}d`;
}

export const KEEP = "__keep__:";

export function isMasked(v: string): boolean {
  return v.startsWith(KEEP);
}

export function maskHint(v: string): string {
  if (!isMasked(v)) return v;
  const rest = v.slice(KEEP.length);
  const i = rest.indexOf(":");
  return i >= 0 ? rest.slice(i + 1) : "****";
}

export function randomKey(prefix = "sk-clr-"): string {
  const b = new Uint8Array(24);
  crypto.getRandomValues(b);
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let s = "";
  for (const x of b) s += alphabet[x % alphabet.length];
  return prefix + s + randomTail();
}

function randomTail(): string {
  const b = new Uint8Array(8);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => (x % 36).toString(36)).join("");
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand("copy");
    } catch {
      ok = false;
    }
    ta.remove();
    return ok;
  }
}

export function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T;
}

export function isErrStatus(s: number): boolean {
  return s >= 400 && s !== 499;
}

/** host:port that a client should use to reach this server, derived from `listen`. */
export function resolveHost(listen: string): string {
  if (!listen) return window.location.host;
  let host = listen;
  let port = "";
  const m = listen.match(/^(.*):(\d+)$/);
  if (m) {
    host = m[1];
    port = m[2];
  }
  host = host.replace(/^\[|\]$/g, "");
  if (!host || host === "0.0.0.0" || host === "::") host = window.location.hostname || "127.0.0.1";
  if (host.includes(":")) host = `[${host}]`;
  return port ? `${host}:${port}` : host;
}

export function endpointURL(listen: string): string {
  return `http://${resolveHost(listen)}`;
}

/** Estimated USD cost: "<$0.01", "$4.20", "$1.2k". */
export function fmtUsd(v: number | undefined | null): string {
  const n = v || 0;
  if (n === 0) return "$0";
  if (n < 0.01) return "<$0.01";
  if (n >= 10000) return `$${(n / 1000).toFixed(0)}k`;
  if (n >= 1000) return `$${(n / 1000).toFixed(1)}k`;
  return `$${n.toFixed(2)}`;
}

/** Per-request cost with more precision ("$0.0123"); "—" when zero/unknown. */
export function fmtUsdFine(v: number | undefined | null): string {
  const n = v || 0;
  if (n <= 0) return "—";
  if (n < 0.0001) return "<$0.0001";
  if (n < 1) return `$${n.toFixed(4)}`;
  return `$${n.toFixed(2)}`;
}

export function isLoopbackListen(listen: string): boolean {
  const h = resolveHost(listen).replace(/:\d+$/, "").replace(/^\[|\]$/g, "");
  return h === "localhost" || h === "::1" || h.startsWith("127.");
}

/** Base URL users should be given: public_url if set, else derived from listen. */
export function publicBase(publicURL: string | undefined, listen: string): string {
  const p = (publicURL || "").trim().replace(/\/+$/, "");
  return p || endpointURL(listen);
}

/** RFC3339 end-of-day (local time) for a yyyy-mm-dd date input; "" for empty. */
export function endOfDayISO(date: string): string {
  if (!date) return "";
  const d = new Date(`${date}T23:59:59`);
  if (isNaN(d.getTime())) return "";
  return d.toISOString();
}

/** yyyy-mm-dd (local) for a date input from an RFC3339 timestamp. */
export function toDateInput(ts: string | undefined): string {
  if (!ts) return "";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}
