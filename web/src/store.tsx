import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { api, ApiError } from "./api";
import { usePoll } from "./hooks";
import { useT, type TFn } from "./i18n";
import type { Config, ModelInfo, Status } from "./types";
import { useToast } from "./ui";

interface StatusCtx {
  status?: Status;
  error?: Error;
  refresh: () => void;
}
const SCtx = createContext<StatusCtx>({ refresh: () => {} });
export const useStatus = () => useContext(SCtx);

export function StatusProvider({ children }: { children: ReactNode }) {
  const p = usePoll((s) => api.status(s), 3000);
  const v = useMemo(() => ({ status: p.data, error: p.error, refresh: p.refresh }), [p.data, p.error, p.refresh]);
  return <SCtx.Provider value={v}>{children}</SCtx.Provider>;
}

export interface SaveResult {
  ok: boolean;
  error?: string;
}

interface ConfigCtx {
  config?: Config;
  error?: Error;
  loading: boolean;
  reload: () => Promise<void>;
  save: (next: Config) => Promise<SaveResult>;
  models: ModelInfo[];
  reloadModels: () => void;
}
const CCtx = createContext<ConfigCtx | null>(null);
export function useConfig(): ConfigCtx {
  const c = useContext(CCtx);
  if (!c) throw new Error("ConfigProvider missing");
  return c;
}

export function ConfigProvider({ children }: { children: ReactNode }) {
  const t = useT();
  const toast = useToast();
  const [config, setConfig] = useState<Config>();
  const [error, setError] = useState<Error>();
  const [loading, setLoading] = useState(true);
  const [models, setModels] = useState<ModelInfo[]>([]);

  const reloadModels = useCallback(() => {
    api.models().then(setModels, () => {});
  }, []);

  const reload = useCallback(async () => {
    try {
      const c = await api.config();
      setConfig(c);
      setError(undefined);
    } catch (e) {
      setError(e as Error);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    reload();
    reloadModels();
  }, [reload, reloadModels]);

  const save = useCallback(
    async (next: Config): Promise<SaveResult> => {
      const before = config;
      try {
        const saved = await api.putConfig(next);
        setConfig(saved);
        reloadModels();
        const changes = before ? diffConfig(before, next, t) : [];
        toast({ kind: "ok", title: t("save.ok"), body: changes.length ? changes.slice(0, 8).join("\n") + (changes.length > 8 ? `\n+${changes.length - 8}…` : "") : t("save.noChanges") });
        return { ok: true };
      } catch (e) {
        const msg = e instanceof ApiError ? e.message : String(e);
        toast({ kind: "err", title: t("save.fail"), body: msg });
        return { ok: false, error: msg };
      }
    },
    [config, reloadModels, t, toast],
  );

  const v = useMemo(() => ({ config, error, loading, reload, save, models, reloadModels }), [config, error, loading, reload, save, models, reloadModels]);
  return <CCtx.Provider value={v}>{children}</CCtx.Provider>;
}

function eq(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

export function diffConfig(a: Config, b: Config, t: TFn): string[] {
  const out: string[] = [];
  const ap = new Map(a.providers.map((p) => [p.id, p]));
  const bp = new Map(b.providers.map((p) => [p.id, p]));
  for (const [id, p] of bp) {
    const old = ap.get(id);
    if (!old) out.push(`+ ${t("diff.provider")} ${id}`);
    else if (!eq(old, p)) {
      const fields = (Object.keys(p) as (keyof typeof p)[]).filter((k) => !eq(old[k], p[k]));
      out.push(`~ ${t("diff.provider")} ${id}: ${fields.join(", ")}`);
    }
  }
  for (const id of ap.keys()) if (!bp.has(id)) out.push(`− ${t("diff.provider")} ${id}`);
  if (!eq(a.providers.map((p) => p.id), b.providers.map((p) => p.id)) && out.length === 0) out.push(`~ ${t("diff.providerOrder")}`);

  const ac = new Map(a.combos.map((c) => [c.name, c]));
  const bc = new Map(b.combos.map((c) => [c.name, c]));
  for (const [n, c] of bc) {
    if (!ac.has(n)) out.push(`+ combo ${n}`);
    else if (!eq(ac.get(n), c)) out.push(`~ combo ${n}`);
  }
  for (const n of ac.keys()) if (!bc.has(n)) out.push(`− combo ${n}`);

  for (const [k, v] of Object.entries(b.aliases)) {
    if (!(k in a.aliases)) out.push(`+ alias ${k} → ${v}`);
    else if (a.aliases[k] !== v) out.push(`~ alias ${k} → ${v}`);
  }
  for (const k of Object.keys(a.aliases)) if (!(k in b.aliases)) out.push(`− alias ${k}`);

  if (a.listen !== b.listen) out.push(`~ listen ${a.listen} → ${b.listen}`);
  if ((a.public_url || "") !== (b.public_url || "")) out.push(`~ public_url ${b.public_url || "–"}`);
  const ak = new Map(a.api_keys.map((k) => [k.name, k]));
  const bk = new Map(b.api_keys.map((k) => [k.name, k]));
  for (const [n, k] of bk) {
    const old = ak.get(n);
    if (!old) out.push(`+ ${t("diff.key")} ${n}`);
    else if (!eq(old, k)) {
      const fields = (Object.keys(k) as (keyof typeof k)[]).filter((f) => !eq(old[f], k[f]));
      out.push(`~ ${t("diff.key")} ${n}: ${fields.join(", ")}`);
    }
  }
  for (const n of ak.keys()) if (!bk.has(n)) out.push(`− ${t("diff.key")} ${n}`);
  const apr = a.pricing || {};
  const bp2 = b.pricing || {};
  for (const m of new Set([...Object.keys(apr), ...Object.keys(bp2)])) {
    if (!(m in bp2)) out.push(`− ${t("diff.price")} ${m}`);
    else if (!(m in apr)) out.push(`+ ${t("diff.price")} ${m}`);
    else if (!eq(apr[m], bp2[m])) out.push(`~ ${t("diff.price")} ${m}`);
  }
  if ((a.admin_password || "") !== (b.admin_password || "")) out.push(`~ admin_password`);
  for (const sec of ["timeouts", "routing", "logging"] as const) {
    const x = a[sec] as unknown as Record<string, unknown>;
    const y = b[sec] as unknown as Record<string, unknown>;
    const ch = Object.keys(y).filter((k) => !eq(x[k], y[k]));
    if (ch.length) out.push(`~ ${sec}: ${ch.join(", ")}`);
  }
  return out;
}
