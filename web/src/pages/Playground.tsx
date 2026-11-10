import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useStored } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { useConfig, useStatus } from "../store";
import { Badge, CopyButton, Empty, ErrorNote } from "../ui";
import { fmtMs } from "../util";

interface Run {
  start: number;
  firstByte?: number;
  firstToken?: number;
  end?: number;
  outTokens?: number;
  inTokens?: number;
  chunks: number;
}

export default function Playground() {
  const t = useT();
  const { status } = useStatus();
  const { config, models } = useConfig();
  const [model, setModel] = useStored<string>("clr.pgModel", "");
  const [key, setKey] = useStored<string>("clr.pgKey", "");
  const [system, setSystem] = useState("");
  const [prompt, setPrompt] = useState("");
  const [out, setOut] = useState("");
  const [reasoning, setReasoning] = useState("");
  const [err, setErr] = useState<string>();
  const [run, setRun] = useState<Run | null>(null);
  const [streaming, setStreaming] = useState(false);
  const [now, setNow] = useState(0);
  const ctrl = useRef<AbortController | null>(null);
  const outRef = useRef<HTMLDivElement>(null);

  const authRequired = status?.auth_required ?? (config ? config.api_keys.length > 0 : false);

  useEffect(() => {
    if (models.length && !models.some((m) => m.id === model)) setModel(models[0].id);
  }, [models, model, setModel]);

  useEffect(() => {
    if (!streaming) return;
    const id = window.setInterval(() => setNow(performance.now()), 100);
    return () => window.clearInterval(id);
  }, [streaming]);

  useEffect(() => () => ctrl.current?.abort(), []);

  useEffect(() => {
    const el = outRef.current;
    if (el && streaming) el.scrollTop = el.scrollHeight;
  }, [out, streaming]);

  const send = async () => {
    if (!prompt.trim() || !model || streaming) return;
    ctrl.current?.abort();
    const c = new AbortController();
    ctrl.current = c;
    setOut("");
    setReasoning("");
    setErr(undefined);
    const r: Run = { start: performance.now(), chunks: 0 };
    setRun({ ...r });
    setStreaming(true);
    setNow(performance.now());
    const messages = [...(system.trim() ? [{ role: "system", content: system }] : []), { role: "user", content: prompt }];
    const headers: Record<string, string> = { "Content-Type": "application/json", Accept: "text/event-stream" };
    if (key.trim()) headers.Authorization = `Bearer ${key.trim()}`;
    let text = "";
    let think = "";
    try {
      const res = await fetch("/v1/chat/completions", {
        method: "POST",
        headers,
        signal: c.signal,
        body: JSON.stringify({ model, stream: true, stream_options: { include_usage: true }, messages }),
      });
      r.firstByte = performance.now();
      setRun({ ...r });
      if (!res.ok || !res.body) {
        const body = await res.text();
        let msg = body;
        try {
          const j = JSON.parse(body);
          msg = j?.error?.message || j?.error || body;
        } catch {
          /* raw text */
        }
        throw new Error(`HTTP ${res.status}: ${msg || res.statusText}`);
      }
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      let done = false;
      while (!done) {
        const { value, done: d } = await reader.read();
        if (d) break;
        buf += dec.decode(value, { stream: true });
        let idx: number;
        while ((idx = buf.search(/\r?\n\r?\n/)) >= 0) {
          const block = buf.slice(0, idx);
          buf = buf.slice(idx).replace(/^\r?\n\r?\n/, "");
          const data = block
            .split(/\r?\n/)
            .filter((l) => l.startsWith("data:"))
            .map((l) => l.slice(5).replace(/^ /, ""))
            .join("\n");
          if (!data) continue;
          if (data === "[DONE]") {
            done = true;
            break;
          }
          let j: {
            choices?: { delta?: { content?: string; reasoning_content?: string; reasoning?: string } }[];
            usage?: { prompt_tokens?: number; completion_tokens?: number };
            error?: { message?: string } | string;
          };
          try {
            j = JSON.parse(data);
          } catch {
            continue;
          }
          if (j.error) throw new Error(typeof j.error === "string" ? j.error : j.error.message || JSON.stringify(j.error));
          const delta = j.choices?.[0]?.delta;
          const piece = delta?.content || "";
          const rpiece = delta?.reasoning_content || delta?.reasoning || "";
          if ((piece || rpiece) && !r.firstToken) r.firstToken = performance.now();
          if (piece) text += piece;
          if (rpiece) think += rpiece;
          if (piece || rpiece) r.chunks++;
          if (j.usage) {
            r.outTokens = j.usage.completion_tokens;
            r.inTokens = j.usage.prompt_tokens;
          }
          setOut(text);
          setReasoning(think);
          setRun({ ...r });
        }
      }
    } catch (e) {
      if ((e as Error).name !== "AbortError") setErr((e as Error).message);
    } finally {
      r.end = performance.now();
      setRun({ ...r });
      setStreaming(false);
      if (ctrl.current === c) ctrl.current = null;
    }
  };

  const stop = () => ctrl.current?.abort();

  const cur = run ? (run.end ?? now) : 0;
  const ttfb = run?.firstByte ? run.firstByte - run.start : undefined;
  const ttft = run?.firstToken ? run.firstToken - run.start : undefined;
  const total = run ? cur - run.start : undefined;
  const genMs = run?.firstToken ? cur - run.firstToken : 0;
  const estTokens = run?.outTokens ?? Math.round((out.length + reasoning.length) / 4);
  const tps = genMs > 200 && estTokens ? estTokens / (genMs / 1000) : undefined;

  if (config && models.length === 0) {
    return (
      <div>
        <div className="page-head">
          <h1>{t("nav.playground")}</h1>
        </div>
        <div className="card">
          <Empty
            icon="playground"
            title={t("pg.noModelsTitle")}
            body={t("pg.noModelsBody")}
            action={
              <Link className="btn primary" to="/providers">
                {t("prov.add")}
              </Link>
            }
          />
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.playground")}</h1>
          <div className="desc">{t("pg.desc")}</div>
        </div>
      </div>
      <div className="split" style={{ alignItems: "start" }}>
        <div className="card card-pad stack">
          <div className="grid-2">
            <div className="field">
              <label htmlFor="pg-model">{t("int.model")}</label>
              <select id="pg-model" className="input mono" value={model} onChange={(e) => setModel(e.target.value)}>
                {models.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.id} ({m.owner})
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="pg-key">{t("pg.key")}</label>
              <input id="pg-key" className="input mono" type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder={authRequired ? "sk-clr-…" : t("pg.keyOptional")} aria-describedby="pg-key-h" />
              <span className="hint" id="pg-key-h">
                {authRequired ? t("pg.keyHint") : t("pg.noAuth")}
              </span>
            </div>
          </div>
          <div className="field">
            <label htmlFor="pg-system">{t("pg.system")}</label>
            <input id="pg-system" className="input" value={system} onChange={(e) => setSystem(e.target.value)} placeholder={t("pg.systemPh")} />
          </div>
          <div className="field">
            <label htmlFor="pg-prompt">{t("pg.prompt")}</label>
            <textarea
              id="pg-prompt"
              className="input"
              style={{ fontFamily: "var(--sans)", fontSize: 13.5, minHeight: 140 }}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder={t("pg.promptPh")}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  send();
                }
              }}
            />
          </div>
          <div className="row">
            {streaming ? (
              <button className="btn danger solid" onClick={stop}>
                <Icon name="stop" size={13} /> {t("pg.stop")}
              </button>
            ) : (
              <button className="btn primary" onClick={send} disabled={!prompt.trim() || !model || (authRequired && !key.trim())}>
                <Icon name="send" size={13} /> {t("pg.send")}
              </button>
            )}
            <span className="faint small hide-sm">
              <kbd>⌘</kbd>/<kbd>Ctrl</kbd> + <kbd>Enter</kbd>
            </span>
          </div>
        </div>

        <div className="card">
          <div className="card-head" style={{ flexWrap: "wrap" }}>
            <h2 className="grow">{t("pg.response")}</h2>
            <span className="row wrap small num" style={{ gap: 6 }} aria-live="off">
              <Badge icon="clock" title={t("pg.ttfbTitle")}>
                TTFB {fmtMs(ttfb)}
              </Badge>
              <Badge title={t("pg.ttftTitle")}>TTFT {fmtMs(ttft)}</Badge>
              <Badge icon="bolt">{tps ? `${tps.toFixed(1)} tok/s` : "– tok/s"}</Badge>
              <Badge>
                {t("pg.total")} {fmtMs(total)}
              </Badge>
              {run?.outTokens != null && (
                <Badge>
                  {run.inTokens ?? "?"} → {run.outTokens} tok
                </Badge>
              )}
            </span>
          </div>
          {err && (
            <div className="card-body" style={{ paddingBottom: 0 }}>
              <ErrorNote error={err} />
            </div>
          )}
          {reasoning && (
            <details className="card-body" style={{ paddingBottom: 0 }}>
              <summary className="faint small">{t("pg.reasoning")}</summary>
              <div className="faint small" style={{ whiteSpace: "pre-wrap", marginTop: 6 }}>
                {reasoning}
              </div>
            </details>
          )}
          <div ref={outRef} className={`playground-out ${streaming ? "caret" : ""}`} aria-live="polite" aria-busy={streaming}>
            {out || (!streaming && !err && <span className="faint">{t("pg.empty")}</span>)}
          </div>
          {out && !streaming && (
            <div className="card-body row" style={{ borderTop: "1px solid var(--border)" }}>
              <CopyButton text={out} />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
