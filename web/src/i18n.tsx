import { createContext, useCallback, useContext, useEffect, useMemo, type ReactNode } from "react";
import { en, vi, type Key } from "./dict";
import { useStored } from "./hooks";

export type Lang = "vi" | "en";
export type TFn = (k: Key, vars?: Record<string, string | number>) => string;

interface Ctx {
  lang: Lang;
  setLang: (l: Lang) => void;
  t: TFn;
}

const LangCtx = createContext<Ctx | null>(null);

function format(s: string, vars?: Record<string, string | number>): string {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

function defaultLang(): Lang {
  try {
    return (navigator.languages || [navigator.language]).some((l) => l?.toLowerCase().startsWith("vi")) ? "vi" : "en";
  } catch {
    return "en";
  }
}

export function LangProvider({ children }: { children: ReactNode }) {
  const [lang, setLang] = useStored<Lang>("clr.lang", defaultLang());
  const t = useCallback<TFn>((k, vars) => format((lang === "en" ? en : vi)[k] ?? k, vars), [lang]);
  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);
  const v = useMemo(() => ({ lang, setLang, t }), [lang, setLang, t]);
  return <LangCtx.Provider value={v}>{children}</LangCtx.Provider>;
}

export function useI18n(): Ctx {
  const c = useContext(LangCtx);
  if (!c) throw new Error("LangProvider missing");
  return c;
}

export function useT(): TFn {
  return useI18n().t;
}
