import { useEffect, useMemo, useState } from "react";
import { useStored } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { useConfig, useStatus } from "../store";
import { CodeBlock } from "../ui";
import { isMasked } from "../util";

type Tool = "claude" | "codex" | "openai" | "gemini" | "curl";

function resolveHost(listen: string): string {
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

export default function Integrations() {
  const t = useT();
  const { status } = useStatus();
  const { config, models } = useConfig();
  const [tool, setTool] = useStored<Tool>("clr.intTool", "claude");
  const [host, setHost] = useState("");
  const [pastedKey, setPastedKey] = useState("");
  const [model, setModel] = useState("");
  const [fast, setFast] = useState("");

  const listen = status?.listen || config?.listen || "";
  useEffect(() => {
    if (listen && !host) setHost(resolveHost(listen));
  }, [listen, host]);

  useEffect(() => {
    if (!model && models.length) {
      setModel(models[0].id);
      const haiku = models.find((m) => /haiku|mini|flash|small/i.test(m.id));
      setFast((haiku || models[0]).id);
    }
  }, [models, model]);

  const first = config?.api_keys[0];
  const authRequired = status?.auth_required ?? (config ? config.api_keys.length > 0 : true);
  const keyInfo = useMemo(() => {
    if (pastedKey.trim()) return { key: pastedKey.trim(), note: "" };
    if (!first) return { key: authRequired ? "<YOUR_CLIROUTER_KEY>" : "clirouter", note: authRequired ? "" : t("int.noAuth") };
    if (first.key.startsWith("env:")) return { key: `$${first.key.slice(4)}`, note: t("int.envKey", { name: first.name, env: first.key.slice(4) }) };
    if (isMasked(first.key)) return { key: "<YOUR_CLIROUTER_KEY>", note: t("int.maskedKey", { name: first.name }) };
    return { key: first.key, note: "" };
  }, [pastedKey, first, authRequired, t]);

  const base = `http://${host || "127.0.0.1:8317"}`;
  const key = keyInfo.key;
  const m = model || "<model>";
  const fm = fast || m;

  const snippets: Record<Tool, { title: string; code: string; note?: string }[]> = {
    claude: [
      {
        title: t("int.shell"),
        code: `export ANTHROPIC_BASE_URL=${base}\nexport ANTHROPIC_AUTH_TOKEN=${key}\nexport ANTHROPIC_MODEL=${m}\nexport ANTHROPIC_SMALL_FAST_MODEL=${fm}\nclaude`,
      },
      {
        title: "~/.claude/settings.json",
        note: t("int.claudeSettings"),
        code: JSON.stringify({ env: { ANTHROPIC_BASE_URL: base, ANTHROPIC_AUTH_TOKEN: key, ANTHROPIC_MODEL: m, ANTHROPIC_SMALL_FAST_MODEL: fm } }, null, 2),
      },
    ],
    codex: [
      {
        title: "~/.codex/config.toml",
        code: `model = "${m}"\nmodel_provider = "clirouter"\n\n[model_providers.clirouter]\nname = "clirouter"\nbase_url = "${base}/v1"\nenv_key = "CLIROUTER_API_KEY"\nwire_api = "responses"`,
      },
      { title: t("int.shell"), code: `export CLIROUTER_API_KEY=${key}\ncodex` },
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
        title: t("int.shell"),
        note: t("int.geminiNote"),
        code: `export GOOGLE_GEMINI_BASE_URL=${base}\nexport GEMINI_API_KEY=${key}\ngemini -m ${m}`,
      },
    ],
    curl: [
      {
        title: "POST /v1/chat/completions",
        code: `curl ${base}/v1/chat/completions \\\n  -H "Authorization: Bearer ${key}" \\\n  -H "Content-Type: application/json" \\\n  -d '{\n    "model": "${m}",\n    "stream": true,\n    "messages": [{"role": "user", "content": "Hello!"}]\n  }'`,
      },
      {
        title: "POST /v1/messages",
        code: `curl ${base}/v1/messages \\\n  -H "x-api-key: ${key}" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -H "Content-Type: application/json" \\\n  -d '{\n    "model": "${m}",\n    "max_tokens": 1024,\n    "messages": [{"role": "user", "content": "Hello!"}]\n  }'`,
      },
      {
        title: "GET /v1/models",
        code: `curl ${base}/v1/models -H "Authorization: Bearer ${key}"`,
      },
    ],
  };

  const tabs: [Tool, string][] = [
    ["claude", "Claude Code"],
    ["codex", "Codex CLI"],
    ["openai", "Cursor / Cline / Continue / opencode"],
    ["gemini", "Gemini CLI"],
    ["curl", "curl"],
  ];

  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.integrations")}</h1>
          <div className="desc">{t("int.desc")}</div>
        </div>
      </div>

      <div className="card card-pad" style={{ marginBottom: 16 }}>
        <div className="grid-2">
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
      </div>

      <div className="tabs" role="tablist">
        {tabs.map(([k, label]) => (
          <button key={k} role="tab" aria-selected={tool === k} onClick={() => setTool(k)}>
            {label}
          </button>
        ))}
      </div>
      <div className="stack">
        {snippets[tool].map((s) => (
          <div key={s.title} className="stack" style={{ marginTop: 0 }}>
            <h3 className="row">
              <Icon name="playground" size={14} /> {s.title}
            </h3>
            {s.note && <p className="muted small">{s.note}</p>}
            <CodeBlock code={s.code} label={s.title} />
          </div>
        ))}
      </div>
    </div>
  );
}
