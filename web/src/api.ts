import type { AccountStatus, ClientKey, Config, KeyPatch, KeyUsage, KeyView, ModelInfo, OAuthAccount, OAuthFlowStatus, OAuthStart, PricingInfo, Provider, ProviderUsage, RecordT, Stats, Status, UsageReport } from "./types";

function normProvider(p: Provider): Provider {
  return {
    ...p,
    keys: p.keys || [],
    oauth: p.oauth || [],
    models: p.models || [],
    model_map: p.model_map || {},
    headers: p.headers || {},
    proxy: p.proxy || "",
    base_url: p.base_url || "",
    priority: p.priority || 0,
    disabled: !!p.disabled,
    first_byte: p.first_byte || 0,
    idle: p.idle || 0,
    budget_usd: p.budget_usd || 0,
    budget_period: p.budget_period || "monthly",
  };
}

function normKey(k: ClientKey): ClientKey {
  return {
    ...k,
    note: k.note || "",
    models: k.models || [],
    budget_usd: k.budget_usd || 0,
    budget_tokens: k.budget_tokens || 0,
    budget_period: k.budget_period || "monthly",
    rpm: k.rpm || 0,
    max_parallel: k.max_parallel || 0,
    expires_at: k.expires_at || "",
    disabled: !!k.disabled,
  };
}

export function normalizeConfig(c: Config): Config {
  return {
    ...c,
    api_keys: (c.api_keys || []).map(normKey),
    providers: (c.providers || []).map(normProvider),
    combos: (c.combos || []).map((x) => ({ ...x, targets: x.targets || [] })),
    aliases: c.aliases || {},
    public_url: c.public_url || "",
    pricing: c.pricing || {},
    alerts: c.alerts || {},
    pricing_sync: c.pricing_sync || {},
  };
}

const PW_KEY = "clr.adminPassword";

export type AuthState = "ok" | "password" | "forbidden";

type Listener = (s: AuthState) => void;
const listeners = new Set<Listener>();
let authState: AuthState = "ok";

export function getAuthState(): AuthState {
  return authState;
}

export function onAuthState(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function setAuthState(s: AuthState) {
  if (authState === s) return;
  authState = s;
  listeners.forEach((l) => l(s));
}

export function getPassword(): string {
  try {
    return localStorage.getItem(PW_KEY) || "";
  } catch {
    return "";
  }
}

export function setPassword(pw: string) {
  try {
    if (pw) localStorage.setItem(PW_KEY, pw);
    else localStorage.removeItem(PW_KEY);
  } catch {
    /* storage unavailable */
  }
  setAuthState("ok");
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function req<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  const pw = getPassword();
  if (pw) headers.Authorization = `Bearer ${pw}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal });
  } catch (e) {
    if ((e as Error).name === "AbortError") throw e;
    throw new ApiError(0, (e as Error).message || "network error");
  }
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    const errField = data && typeof data === "object" ? (data as { error?: unknown }).error : undefined;
    const msg: string = errField ? String(errField) : text || res.statusText;
    if (res.status === 401) setAuthState("password");
    if (res.status === 403) setAuthState("forbidden");
    throw new ApiError(res.status, msg);
  }
  if (authState !== "ok") setAuthState("ok");
  if (data === null && text) throw new ApiError(res.status, "invalid JSON response");
  return data as T;
}

const enc = encodeURIComponent;

export const api = {
  status: (s?: AbortSignal) => req<Status>("GET", "/admin/api/status", undefined, s),
  config: (s?: AbortSignal) => req<Config>("GET", "/admin/api/config", undefined, s).then(normalizeConfig),
  putConfig: (c: Config) => req<Config>("PUT", "/admin/api/config", c).then(normalizeConfig),
  accounts: (s?: AbortSignal) => req<AccountStatus[]>("GET", "/admin/api/accounts", undefined, s),
  refreshQuota: (id: string) => req<AccountStatus>("POST", `/admin/api/accounts/${enc(id)}/quota`),
  resetAccount: (id: string) => req<AccountStatus>("POST", `/admin/api/accounts/${enc(id)}/reset`),
  requests: (q: { limit?: number; before?: number; provider?: string; model?: string; errors?: boolean }, s?: AbortSignal) => {
    const p = new URLSearchParams();
    if (q.limit) p.set("limit", String(q.limit));
    if (q.before) p.set("before", String(q.before));
    if (q.provider) p.set("provider", q.provider);
    if (q.model) p.set("model", q.model);
    if (q.errors) p.set("errors", "1");
    return req<RecordT[]>("GET", `/admin/api/requests?${p}`, undefined, s);
  },
  live: (n = 100, s?: AbortSignal) => req<RecordT[]>("GET", `/admin/api/live?n=${n}`, undefined, s),
  stats: (range: string, s?: AbortSignal) =>
    req<Stats>("GET", `/admin/api/stats?range=${enc(range)}`, undefined, s).then((x) => ({
      ...x,
      by_model: x.by_model || [],
      by_provider: x.by_provider || [],
      by_key: x.by_key || [],
      timeline: x.timeline || [],
    })),
  createKey: (p: KeyPatch) => req<KeyView>("POST", "/admin/api/keys", p),
  updateKey: (name: string, p: KeyPatch) => req<KeyView>("PATCH", `/admin/api/keys/${enc(name)}`, p),
  rotateKey: (name: string) => req<KeyView>("POST", `/admin/api/keys/${enc(name)}/rotate`),
  deleteKey: (name: string) => req<{ ok: boolean }>("DELETE", `/admin/api/keys/${enc(name)}`),
  testAlert: () => req<{ ok: boolean }>("POST", "/admin/api/alerts/test"),
  syncPricing: () => req<{ source: string; updated_at: string; count: number }>("POST", "/admin/api/pricing/sync"),
  keysUsage: (s?: AbortSignal) => req<KeyUsage[] | null>("GET", "/admin/api/keys/usage", undefined, s).then((x) => x || []),
  providersUsage: (s?: AbortSignal) => req<ProviderUsage[] | null>("GET", "/admin/api/providers/usage", undefined, s).then((x) => x || []),
  usage: (q: { range: string; group: string; key?: string }, s?: AbortSignal) => {
    const p = new URLSearchParams({ range: q.range, group: q.group });
    if (q.key) p.set("key", q.key);
    return req<UsageReport>("GET", `/admin/api/usage?${p}`, undefined, s).then((x) => ({ ...x, rows: x.rows || [], daily: x.daily || [] }));
  },
  pricing: (s?: AbortSignal) => req<PricingInfo>("GET", "/admin/api/pricing", undefined, s).then((x) => ({ builtin: x.builtin || {}, overrides: x.overrides || {}, synced: x.synced })),
  models: (s?: AbortSignal) => req<ModelInfo[]>("GET", "/admin/api/models", undefined, s),
  oauthStart: (b: { type: string; account_id?: string; project_id?: string }) => req<OAuthStart>("POST", "/admin/api/oauth/start", b),
  oauthComplete: (flow_id: string, code: string) => req<OAuthAccount>("POST", "/admin/api/oauth/complete", { flow_id, code }),
  oauthStatus: (flow_id: string, s?: AbortSignal) => req<OAuthFlowStatus>("GET", `/admin/api/oauth/status?flow_id=${enc(flow_id)}`, undefined, s),
  oauthAccounts: (s?: AbortSignal) => req<OAuthAccount[] | null>("GET", "/admin/api/oauth/accounts", undefined, s).then((x) => x || []),
  oauthDelete: (id: string) => req<{ ok: boolean }>("DELETE", `/admin/api/oauth/accounts/${enc(id)}`),
};
