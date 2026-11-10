import { useCallback, useEffect, useRef, useState, type RefObject } from "react";

export function usePageVisible(): boolean {
  const [visible, setVisible] = useState(() => typeof document === "undefined" || !document.hidden);
  useEffect(() => {
    const on = () => setVisible(!document.hidden);
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, []);
  return visible;
}

export interface PollState<T> {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  refresh: () => void;
  updatedAt: number;
}

export function usePoll<T>(fn: (signal: AbortSignal) => Promise<T>, intervalMs: number, deps: unknown[] = [], enabled = true): PollState<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<Error>();
  const [loading, setLoading] = useState(true);
  const [updatedAt, setUpdatedAt] = useState(0);
  const [tick, setTick] = useState(0);
  const visible = usePageVisible();
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    if (!enabled || !visible) return;
    let alive = true;
    let timer: number | undefined;
    let ctrl: AbortController | undefined;
    const run = async () => {
      ctrl = new AbortController();
      try {
        const v = await fnRef.current(ctrl.signal);
        if (!alive) return;
        setData(v);
        setError(undefined);
        setUpdatedAt(Date.now());
      } catch (e) {
        if (!alive || (e as Error).name === "AbortError") return;
        setError(e as Error);
      } finally {
        if (alive) {
          setLoading(false);
          if (intervalMs > 0) timer = window.setTimeout(run, intervalMs);
        }
      }
    };
    run();
    return () => {
      alive = false;
      window.clearTimeout(timer);
      ctrl?.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, visible, intervalMs, tick, ...deps]);

  const refresh = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, loading, refresh, updatedAt };
}

export function useNow(intervalMs = 1000, enabled = true): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs, enabled]);
  return now;
}

export function useStored<T extends string>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      return (localStorage.getItem(key) as T) || initial;
    } catch {
      return initial;
    }
  });
  const set = useCallback(
    (nv: T) => {
      setV(nv);
      try {
        localStorage.setItem(key, nv);
      } catch {
        /* ignore */
      }
    },
    [key],
  );
  return [v, set];
}

export function useWidth<E extends HTMLElement>(): [RefObject<E>, number] {
  const ref = useRef<E>(null);
  const [w, setW] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) setW(Math.floor(e.contentRect.width));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}
