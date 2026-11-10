import { useEffect, useMemo, useState } from "react";
import { useStored } from "../hooks";
import { useT } from "../i18n";
import { Icon, type IconName } from "../icons";
import { useConfig, useStatus } from "../store";
import { CopyButton } from "../ui";
import { fmtMs, isMasked, resolveHost } from "../util";

type Tool = "claude" | "codex" | "openai" | "gemini" | "curl";
type OS = "unix" | "windows";

const TOOLS: { id: Tool; name: string; proto: string; icon: IconName }[] = [
  { id: "claude", name: "Claude Code", proto: "/v1/messages", icon: "playground" },
  { id: "codex", name: "Codex CLI", proto: "/v1/responses", icon: "playground" },
  { id: "openai", name: "Cursor · Cline · opencode", proto: "/v1/chat/completions", icon: "integrations" },
  { id: "gemini", name: "Gemini CLI", proto: "/v1beta", icon: "playground" },
  { id: "curl", name: "curl / SDK", proto: "HTTP", icon: "send" },
];

interface Snippet {
  title: string;
  code: string;
  note?: string;
}

/** Shell-specific "set env var" line. */
function setEnv(os: OS, name: string, value: string): string {
  return os === "windows" ? `$env:${name} = "${value}"` : `export ${name}=${value}`;
}

type TestState = { state: "idle" } | { state: "busy" } | { state: "ok"; models: number; ms: number } | { state: "err"; msg: string };

export default function Integrations() {
  const t = useT();
  const { status } = useStatus();
  const { config, models } = useConfig();
  const [tool, setTool] = useStored<Tool>("clr.intTool", "claude");
  const [os, setOS] = useStored<OS>("clr.intOS", /Win/i.test(navigator.userAgent) ? "windows" : "unix");
  const [, setVisited] = useStored<"0" | "1">("clr.visitedIntegrations", "0");
  const [host, setHost] = useState("");
  const [pastedKey, setPastedKey] = useState("");
  const [model, setModel] = useState("");
  const [fast, setFast] = useState("");
  const [test, setTest] = useState<TestState>({ state: "idle" });

  useEffect(() => setVisited("1"), [setVisited]);

  const listen = status?.listen || config?.listen || "";
  useEffect(() => {
    if (listen && !host) setHost(resolveHost(listen));
  }, [listen, host]);

  useEffect(() => {
    if (!model && models.length) {
      setModel(models[0].id);
      const small = models.find((m) => /haiku|mini|flash|small|lite/i.test(m.id));
      setFast((small || models[0]).id);
    }
  }, [models, model]);

  const first = config?.api_keys[0];
  const authRequired = status?.auth_required ?? (config ? config.api_keys.length > 0 : true);
  const keyInfo = useMemo(() => {
    if (pastedKey.trim()) return { key: pastedKey.trim(), note: "", real: true };
    if (!first) return { key: authRequired ? "<YOUR_CLIROUTER_KEY>" : "clirouter", note: authRequired ? "" : t("int.noAuth"), real: !authRequired };
    if (first.key.startsWith("env:")) return { key: os === "windows" ? `$env:${first.key.slice(4)}` : `$${first.key.slice(4)}`, note: t("int.envKey", { name: first.name, env: first.key.slice(4) }), real: false };
    if (isMasked(first.key)) return { key: "<YOUR_CLIROUTER_KEY>", note: t("int.maskedKey", { name: first.name }), real: false };
    return { key: first.key, note: "", real: true };
  }, [pastedKey, first, authRequired, t, os]);

  const base = `http://${host || "127.0.0.1:8317"}`;
  const key = keyInfo.key;
  const m = model || "<model>";
  const fm = fast || m;
  const win = os === "windows";
  const env = (n: string, v: string) => setEnv(os, n, v);
  const cont = win ? "`" : "\\";

  const snippets: Record<Tool, Snippet[]> = {
    claude: [
      {
        title: win ? "PowerShell" : "Shell (macOS / Linux)",
        note: t("int.claudeShell"),
        code: [
          env("ANTHROPIC_BASE_URL", base),
          env("ANTHROPIC_AUTH_TOKEN", key),
          env("ANTHROPIC_MODEL", m),
          env("ANTHROPIC_DEFAULT_OPUS_MODEL", m),
          env("ANTHROPIC_DEFAULT_SONNET_MODEL", m),
          env("ANTHROPIC_DEFAULT_HAIKU_MODEL", fm),
          "claude",
        ].join("\n"),
      },
      {
        title: win ? "%USERPROFILE%\\.claude\\settings.json" : "~/.claude/settings.json",
        note: t("int.claudeSettings"),
        code: JSON.stringify(
          {
            env: {
              ANTHROPIC_BASE_URL: base,
              ANTHROPIC_AUTH_TOKEN: key,
              ANTHROPIC_MODEL: m,
              ANTHROPIC_DEFAULT_OPUS_MODEL: m,
              ANTHROPIC_DEFAULT_SONNET_MODEL: m,
              ANTHROPIC_DEFAULT_HAIKU_MODEL: fm,
            },
          },
          null,
          2,
        ),
      },
    ],
    codex: [
      {
        title: win ? "%USERPROFILE%\\.codex\\config.toml" : "~/.codex/config.toml",
        code: `model = "${m}"\nmodel_provider = "clirouter"\n\n[model_providers.clirouter]\nname = "clirouter"\nbase_url = "${base}/v1"\nenv_key = "CLIROUTER_API_KEY"\nwire_api = "responses"`,
      },
      { title: win ? "PowerShell" : "Shell (macOS / Linux)", code: `${env("CLIROUTER_API_KEY", key)}\ncodex` },
    ],
    openai: [
      {
        title: t("int.genericOpenAI"),
        note: t("int.openaiNote"),
        code: `Base URL:  ${base}/v1\nAPI key:   ${key}\nModel:     ${m}`,
      },
      {
        title: "opencode — opencode.json",
        code: JSON.stringify(
          {
            $schema: "https://opencode.ai/config.json",
            provider: {
              clirouter: {
                npm: "@ai-sdk/openai-compatible",
                name: "clirouter",
                options: { baseURL: `${base}/v1`, apiKey: key },
                models: Object.fromEntries((models.length ? models.slice(0, 8).map((x) => x.id) : [m]).map((id) => [id, { name: id }])),
              },
            },
          },
          null,
          2,
        ),
      },
      {
        title: "Continue — ~/.continue/config.yaml",
        code: `models:\n  - name: clirouter ${m}\n    provider: openai\n    model: ${m}\n    apiBase: ${base}/v1\n    apiKey: ${key}`,
      },
    ],
    gemini: [
      {
        title: win ? "PowerShell" : "Shell (macOS / Linux)",
        note: t("int.geminiNote"),
        code: `${env("GOOGLE_GEMINI_BASE_URL", base)}\n${env("GEMINI_API_KEY", key)}\ngemini -m ${m}`,
      },
    ],
    curl: [
      {
        title: "POST /v1/chat/completions",
        code: win
          ? `$body = @{\n  model = "${m}"\n  messages = @(@{ role = "user"; content = "Hello!" })\n} | ConvertTo-Json -Depth 5\nInvoke-RestMethod -Method Post -Uri "${base}/v1/chat/completions" ${cont}\n  -Headers @{ Authorization = "Bearer ${key}" } ${cont}\n  -ContentType "application/json" -Body $body`
          : `curl ${base}/v1/chat/completions \\\n  -H "Authorization: Bearer ${key}" \\\n  -H "Content-Type: application/json" \\\n  -d '{\n    "model": "${m}",\n    "stream": true,\n    "messages": [{"role": "user", "content": "Hello!"}]\n  }'`,
      },
      {
        title: "POST /v1/messages",
        code: win
          ? `$body = @{\n  model = "${m}"\n  max_tokens = 1024\n  messages = @(@{ role = "user"; content = "Hello!" })\n} | ConvertTo-Json -Depth 5\nInvoke-RestMethod -Method Post -Uri "${base}/v1/messages" ${cont}\n  -Headers @{ "x-api-key" = "${key}"; "anthropic-version" = "2023-06-01" } ${cont}\n  -ContentType "application/json" -Body $body`
          : `curl ${base}/v1/messages \\\n  -H "x-api-key: ${key}" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -H "Content-Type: application/json" \\\n  -d '{\n    "model": "${m}",\n    "max_tokens": 1024,\n    "messages": [{"role": "user", "content": "Hello!"}]\n  }'`,
      },
      {
        title: "Python — openai SDK",
        code: `from openai import OpenAI\n\nclient = OpenAI(base_url="${base}/v1", api_key="${key}")\nr = client.chat.completions.create(\n    model="${m}",\n    messages=[{"role": "user", "content": "Hello!"}],\n)\nprint(r.choices[0].message.content)`,
      },
      {
        title: "GET /v1/models",
        code: `${win ? "curl.exe" : "curl"} ${base}/v1/models -H "Authorization: Bearer ${key}"`,
      },
    ],
  };

  // Runs from this browser against the same server; proves the key is accepted and models resolve.
  const runTest = async () => {
    setTest({ state: "busy" });
    const t0 = performance.now();
    try {
      const headers: Record<string, string> = {};
      if (keyInfo.real) headers.Authorization = `Bearer ${key}`;
      const res = await fetch("/v1/models", { headers });
      const body = await res.text();
      if (!res.ok) {
        let msg = body;
        try {
          const j = JSON.parse(body);
          msg = j?.error?.message || j?.error || body;
        } catch {
          /* plain text */
        }
        setTest({ state: "err", msg: `HTTP ${res.status}: ${String(msg).slice(0, 200)}` });
        return;
      }
      const j = JSON.parse(body);
      setTest({ state: "ok", models: Array.isArray(j?.data) ? j.data.length : 0, ms: performance.now() - t0 });
    } catch (e) {
      setTest({ state: "err", msg: (e as Error).message });
    }
  };
  const needKey = authRequired && !keyInfo.real;

  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.integrations")}</h1>
          <div className="desc">{t("int.desc")}</div>
        </div>
        <div className="seg" role="group" aria-label={t("int.os")}>
          <button aria-pressed={!win} onClick={() => setOS("unix")}>
            macOS / Linux
          </button>
          <button aria-pressed={win} onClick={() => setOS("windows")}>
            Windows
          </button>
        </div>
      </div>

      <div className="tool-grid" role="tablist" aria-label={t("int.tool")}>
        {TOOLS.map((x) => (
          <button key={x.id} role="tab" className="tool" aria-selected={tool === x.id} onClick={() => setTool(x.id)}>
            <span className="t-name">
              <Icon name={x.icon} size={14} />
              {x.name}
            </span>
            <span className="t-proto">{x.proto}</span>
          </button>
        ))}
      </div>

      <section className="card" style={{ marginBottom: 18 }}>
        <div className="card-head">
          <h2 className="grow">{t("int.params")}</h2>
          <div className="test-result" role="status">
            {test.state === "ok" && (
              <span className="badge ok">
                <Icon name="check" size={12} strokeWidth={2.5} />
                {t("int.testOk", { n: test.models, ms: fmtMs(test.ms) })}
              </span>
            )}
            {test.state === "err" && (
              <span className="badge err" title={test.msg}>
                <Icon name="alert" size={12} />
                {t("int.testFail")}
              </span>
            )}
          </div>
          <button className="btn sm" onClick={runTest} disabled={test.state === "busy" || needKey} title={needKey ? t("int.testNeedKey") : undefined}>
            <Icon name="bolt" size={13} />
            {test.state === "busy" ? t("int.testing") : t("int.test")}
          </button>
        </div>
        <div className="card-body grid-2">
          <div className="field">
            <label htmlFor="int-host">{t("int.host")}</label>
            <input id="int-host" className="input mono" value={host} onChange={(e) => setHost(e.target.value)} aria-describedby="int-host-h" />
            <span className="hint" id="int-host-h">
              {t("int.hostHint", { listen: listen || "–" })}
            </span>
          </div>
          <div className="field">
            <label htmlFor="int-key">{t("int.key")}</label>
            <input id="int-key" className="input mono" type="password" autoComplete="off" value={pastedKey} placeholder={first ? first.name : t("int.keyPh")} onChange={(e) => setPastedKey(e.target.value)} aria-describedby="int-key-h" />
            <span className="hint" id="int-key-h">
              {keyInfo.note || t("int.keyHint")}
            </span>
          </div>
          <div className="field">
            <label htmlFor="int-model">{t("int.model")}</label>
            <select id="int-model" className="input mono" value={model} onChange={(e) => setModel(e.target.value)}>
              {models.length === 0 && <option value="">{t("int.noModels")}</option>}
              {models.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.id} ({x.owner})
                </option>
              ))}
            </select>
          </div>
          {tool === "claude" && (
            <div className="field">
              <label htmlFor="int-fast">{t("int.fastModel")}</label>
              <select id="int-fast" className="input mono" value={fast} onChange={(e) => setFast(e.target.value)}>
                {models.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.id} ({x.owner})
                  </option>
                ))}
              </select>
            </div>
          )}
        </div>
        {test.state === "err" && (
          <div style={{ padding: "0 18px 18px" }}>
            <div className="alert err" role="alert">
              <Icon name="alert" style={{ flex: "none", marginTop: 2 }} />
              <pre>{test.msg}</pre>
            </div>
          </div>
        )}
      </section>

      <div className="stack">
        {snippets[tool].map((s) => (
          <div key={s.title} className="snippet">
            <div className="snippet-head">
              <Icon name={s.title.includes("/") || s.title.includes("\\") || s.title.includes(".") ? "edit" : "playground"} size={14} className="faint" />
              <span className="s-title grow ellipsis">{s.title}</span>
              <CopyButton text={s.code} className="btn xs" />
            </div>
            {s.note && <p className="snippet-note">{s.note}</p>}
            <pre className="code" tabIndex={0} aria-label={s.title}>
              {s.code}
            </pre>
          </div>
        ))}
      </div>
    </div>
  );
}
