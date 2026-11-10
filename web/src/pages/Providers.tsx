import { useMemo, useState } from "react";
import { api } from "../api";
import type { Key } from "../dict";
import { usePoll } from "../hooks";
import { ChipsInput, fromRows, KVEditor, rowsError, toRows, type KVRows } from "../editors";
import { useT, type TFn } from "../i18n";
import { Icon } from "../icons";
import { useConfig } from "../store";
import { BUDGET_PERIODS, bedrockRegion, HINT_TYPES, KEYLESS_TYPES, OAUTH_TYPE_MAP, OAUTH_TYPES, PROVIDER_TYPES, REGION_TYPES, type BudgetPeriod, type Config, type Provider, type ProviderUsage } from "../types";
import { OAUTH_LABEL, OAuthFlow, useSubscriptionConsent } from "../oauth";
import { Badge, Drawer, Empty, ErrorNote, Field, Meter, Switch, useConfirm, useToast } from "../ui";
import { clone, fmtDuration, fmtUsd, isMasked, maskHint, parseDuration } from "../util";

interface Form {
  origId: string | null;
  id: string;
  type: string;
  base_url: string;
  keys: string[];
  newKeys: string;
  oauth: string[];
  models: string[];
  model_map: KVRows;
  headers: KVRows;
  proxy: string;
  priority: string;
  disabled: boolean;
  first_byte: string;
  idle: string;
  budget_usd: string;
  budget_period: BudgetPeriod;
  region: string;
  project: string;
}

function toForm(p: Provider | null): Form {
  return {
    origId: p ? p.id : null,
    id: p?.id || "",
    type: p?.type || "anthropic",
    base_url: p?.base_url || "",
    keys: p ? [...p.keys] : [],
    newKeys: "",
    oauth: p ? [...p.oauth] : [],
    models: p ? [...p.models] : [],
    model_map: p ? toRows(p.model_map) : [],
    headers: p ? toRows(p.headers) : [],
    proxy: p?.proxy || "",
    priority: p?.priority ? String(p.priority) : "",
    disabled: !!p?.disabled,
    first_byte: p ? fmtDuration(p.first_byte) : "",
    idle: p ? fmtDuration(p.idle) : "",
    budget_usd: p?.budget_usd ? String(p.budget_usd) : "",
    budget_period: p?.budget_period || "monthly",
    region: p?.region || "",
    project: p?.project || "",
  };
}

function splitKeys(s: string): string[] {
  return s
    .split(/\r?\n/)
    .map((x) => x.trim())
    .filter(Boolean);
}

function mergeKeys(keys: string[], extra: string): string[] {
  const out = [...keys];
  for (const k of splitKeys(extra)) if (!out.includes(k)) out.push(k);
  return out;
}

type Errors = Partial<Record<"id" | "type" | "base_url" | "first_byte" | "idle" | "priority" | "model_map" | "headers" | "keys" | "budget_usd" | "region", string>>;

/** Key strings that are references, not secrets, and can be shown in full. */
function isKeyRef(k: string): boolean {
  return k.startsWith("env:") || k.startsWith("sa:") || k === "aws:env";
}

function validate(f: Form, cfg: Config, t: TFn): Errors {
  const e: Errors = {};
  const id = f.id.trim();
  if (!id) e.id = t("val.required");
  else if (id.includes("/")) e.id = t("val.noSlash");
  else if (id !== f.origId && cfg.providers.some((p) => p.id === id)) e.id = t("val.idTaken");
  else if (cfg.combos.some((c) => c.name === id)) e.id = t("val.idCombo");
  if (!(f.type in PROVIDER_TYPES)) e.type = t("val.required");
  if (!f.base_url.trim() && !PROVIDER_TYPES[f.type]) e.base_url = t("val.baseUrlRequired");
  else if (f.base_url.trim() && !/^https?:\/\//.test(f.base_url.trim())) e.base_url = t("val.url");
  else if (f.type === "bedrock" && !f.region.trim() && !bedrockRegion(f.base_url)) e.base_url = t("val.bedrockRegion");
  if (REGION_TYPES.has(f.type) && f.region.trim() && !/^[a-z0-9][a-z0-9-]*$/.test(f.region.trim())) e.region = t("val.region");
  if (parseDuration(f.first_byte) === null) e.first_byte = t("val.duration");
  if (parseDuration(f.idle) === null) e.idle = t("val.duration");
  if (f.priority.trim() && !/^-?\d+$/.test(f.priority.trim())) e.priority = t("val.int");
  if (f.budget_usd.trim() && !(Number.isFinite(Number(f.budget_usd)) && Number(f.budget_usd) >= 0)) e.budget_usd = t("val.nonNeg");
  const mm = rowsError(f.model_map, t);
  if (mm) e.model_map = mm;
  const hh = rowsError(f.headers, t);
  if (hh) e.headers = hh;
  return e;
}

function fromForm(f: Form, base: Provider | null): Provider {
  return {
    ...(base || ({} as Provider)),
    id: f.id.trim(),
    type: f.type,
    base_url: f.base_url.trim(),
    keys: mergeKeys(f.keys, f.newKeys),
    oauth: f.oauth,
    models: f.models,
    model_map: fromRows(f.model_map),
    headers: fromRows(f.headers),
    proxy: f.proxy.trim(),
    priority: f.priority.trim() ? parseInt(f.priority, 10) : 0,
    disabled: f.disabled,
    first_byte: parseDuration(f.first_byte) || 0,
    idle: parseDuration(f.idle) || 0,
    budget_usd: f.budget_usd.trim() ? Number(f.budget_usd) : 0,
    budget_period: f.budget_period,
    region: REGION_TYPES.has(f.type) ? f.region.trim() || undefined : undefined,
    project: f.type === "vertex" ? f.project.trim() || undefined : undefined,
  };
}

export default function Providers() {
  const t = useT();
  const confirm = useConfirm();
  const { config, error, loading, save } = useConfig();
  const [editing, setEditing] = useState<Form | null>(null);
  const [q, setQ] = useState("");
  const spend = usePoll((s) => api.providersUsage(s), 10000);
  const spendBy = useMemo(() => new Map((spend.data || []).map((u) => [u.id, u])), [spend.data]);

  const list = useMemo(() => {
    const ps = config?.providers || [];
    const s = q.trim().toLowerCase();
    return s ? ps.filter((p) => p.id.toLowerCase().includes(s) || p.type.includes(s) || p.models.some((m) => m.toLowerCase().includes(s))) : ps;
  }, [config, q]);

  if (loading && !config) return <div className="faint">{t("common.loading")}</div>;
  if (!config) return <ErrorNote error={error} />;

  const toggle = async (p: Provider) => {
    const next = clone(config);
    next.providers = next.providers.map((x) => (x.id === p.id ? { ...x, disabled: !x.disabled } : x));
    await save(next);
  };

  const remove = async (p: Provider) => {
    const ok = await confirm({ title: t("prov.deleteTitle", { id: p.id }), body: t("prov.deleteBody", { n: p.keys.length + p.oauth.length }), confirm: t("common.delete"), danger: true });
    if (!ok) return;
    const next = clone(config);
    next.providers = next.providers.filter((x) => x.id !== p.id);
    await save(next);
  };

  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.providers")}</h1>
          <div className="desc">{t("prov.desc")}</div>
        </div>
        {config.providers.length > 3 && (
          <input className="input" style={{ width: 200 }} type="search" placeholder={t("common.search")} aria-label={t("common.search")} value={q} onChange={(e) => setQ(e.target.value)} />
        )}
        <button className="btn primary" onClick={() => setEditing(toForm(null))}>
          <Icon name="plus" size={14} /> {t("prov.add")}
        </button>
      </div>

      {config.providers.length === 0 ? (
        <div className="card">
          <Empty
            icon="providers"
            title={t("prov.emptyTitle")}
            body={t("prov.emptyBody")}
            action={
              <button className="btn primary" onClick={() => setEditing(toForm(null))}>
                <Icon name="plus" size={14} /> {t("prov.add")}
              </button>
            }
          />
        </div>
      ) : (
        <div className="provider-grid">
          {list.map((p) => (
            <ProviderCard key={p.id} p={p} usage={spendBy.get(p.id)} onEdit={() => setEditing(toForm(p))} onToggle={() => toggle(p)} onDelete={() => remove(p)} />
          ))}
          {list.length === 0 && <div className="faint">{t("common.noMatch")}</div>}
        </div>
      )}

      {editing && <ProviderDrawer form={editing} setForm={setEditing} config={config} save={save} onClose={() => setEditing(null)} />}
    </div>
  );
}

const TYPE_COLOR: Record<string, string> = {
  anthropic: "#c96442",
  "claude-oauth": "#b4532f",
  openai: "#10a37f",
  codex: "#0f766e",
  "openai-compatible": "#5046e5",
  gemini: "#2f6fe4",
  "gemini-cli": "#1a5fd0",
  vertex: "#188038",
  copilot: "#6e40c9",
  "azure-openai": "#0a6ac2",
  bedrock: "#b45309",
  ollama: "#3f3f46",
};

const KEY_PLACEHOLDER: Record<string, string> = {
  bedrock: "env:AWS_BEARER_TOKEN_BEDROCK\naws:env\naws:AKIA…:secret",
  vertex: "sa:/path/to/service-account.json\nsa:env:GOOGLE_SA_JSON\nenv:VERTEX_API_KEY",
  "azure-openai": "env:AZURE_OPENAI_API_KEY",
  ollama: "(optional)",
};

function ProviderAvatar({ p }: { p: Provider }) {
  const letters = p.id.replace(/[^a-z0-9]/gi, "").slice(0, 2) || "?";
  return (
    <span className="prov-avatar" style={{ background: TYPE_COLOR[p.type] || "#64748b" }} aria-hidden="true">
      {letters}
    </span>
  );
}

function ProviderCard({ p, usage, onEdit, onToggle, onDelete }: { p: Provider; usage?: ProviderUsage; onEdit: () => void; onToggle: () => void; onDelete: () => void }) {
  const t = useT();
  const shown = p.models.slice(0, 6);
  const mapped = Object.keys(p.model_map);
  return (
    <div className={`card prov-card ${p.disabled ? "disabled" : ""}`}>
      <div className="row" style={{ gap: 12 }}>
        <ProviderAvatar p={p} />
        <div className="grow" style={{ minWidth: 0 }}>
          <h2 className="ellipsis mono" style={{ fontSize: 14 }}>
            {p.id}
          </h2>
          <div className="row" style={{ gap: 6, marginTop: 3 }}>
            <Badge tone="accent">{p.type}</Badge>
            {p.disabled && <Badge icon="pause">{t("prov.disabled")}</Badge>}
            {usage?.over_budget && (
              <Badge tone="err" icon="alert">
                {t("prov.overBudget")}
              </Badge>
            )}
          </div>
        </div>
        <Switch checked={!p.disabled} onChange={onToggle} label={<span className="sr-only">{t("prov.enableX", { id: p.id })}</span>} />
      </div>
      <dl className="prov-meta">
        <dt>{t("prov.baseUrl")}</dt>
        <dd className="mono ellipsis" title={p.base_url}>
          {p.base_url || <span className="faint">{t("prov.default")}</span>}
        </dd>
        <dt>{t("prov.creds")}</dt>
        <dd>
          {p.keys.length} {p.keys.length === 1 ? t("prov.keyLc") : t("prov.keysLc")}
          {p.oauth.length > 0 && ` · ${p.oauth.length} OAuth`}
          {p.keys.length + p.oauth.length === 0 &&
            (KEYLESS_TYPES.has(p.type) ? (
              <span className="faint small">{t("prov.keyless")}</span>
            ) : (
              <Badge tone="warn" icon="alert">
                {t("prov.noCreds")}
              </Badge>
            ))}
        </dd>
        {p.priority !== 0 && (
          <>
            <dt>{t("prov.priority")}</dt>
            <dd className="num">{p.priority}</dd>
          </>
        )}
        {(p.first_byte > 0 || p.idle > 0) && (
          <>
            <dt>{t("prov.timeouts")}</dt>
            <dd className="mono">
              {p.first_byte > 0 && `first_byte ${fmtDuration(p.first_byte)} `}
              {p.idle > 0 && `idle ${fmtDuration(p.idle)}`}
            </dd>
          </>
        )}
        {p.region && (
          <>
            <dt>{t("prov.region")}</dt>
            <dd className="mono ellipsis">{p.region}</dd>
          </>
        )}
        {p.project && (
          <>
            <dt>{t("prov.project")}</dt>
            <dd className="mono ellipsis">{p.project}</dd>
          </>
        )}
        {p.proxy && (
          <>
            <dt>Proxy</dt>
            <dd className="mono ellipsis">{p.proxy}</dd>
          </>
        )}
        {(p.budget_usd || 0) > 0 && (
          <>
            <dt>{t("prov.spend")}</dt>
            <dd>
              <Meter
                pct={usage ? (usage.cost_usd / (p.budget_usd || 1)) * 100 : 0}
                value={`${fmtUsd(usage?.cost_usd || 0)} / ${fmtUsd(p.budget_usd)}`}
                sub={t(`keys.per.${p.budget_period || "monthly"}` as Key)}
                label={t("prov.spend")}
              />
            </dd>
          </>
        )}
      </dl>
      <div className="row wrap" style={{ gap: 4 }}>
        {shown.map((m) => (
          <span key={m} className="chip static">
            {m}
          </span>
        ))}
        {p.models.length > shown.length && <span className="faint small">+{p.models.length - shown.length}</span>}
        {mapped.length > 0 && <span className="faint small">· {t("prov.mapped", { n: mapped.length })}</span>}
        {p.models.length === 0 && mapped.length === 0 && <span className="faint small">{t("prov.anyModel")}</span>}
      </div>
      <div className="row" style={{ marginTop: "auto" }}>
        <button className="btn sm" onClick={onEdit}>
          <Icon name="edit" size={13} /> {t("common.edit")}
        </button>
        <span className="spacer" />
        <button className="btn sm ghost danger" onClick={onDelete} aria-label={t("prov.deleteTitle", { id: p.id })}>
          <Icon name="trash" size={13} />
          {t("common.delete")}
        </button>
      </div>
    </div>
  );
}

function ProviderDrawer({ form, setForm, config, save, onClose }: { form: Form; setForm: (f: Form) => void; config: Config; save: ReturnType<typeof useConfig>["save"]; onClose: () => void }) {
  const t = useT();
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [serverErr, setServerErr] = useState<string>();
  const errs = validate(form, config, t);
  const show = (k: keyof Errors) => (submitted || k === "keys" ? errs[k] : undefined);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm({ ...form, [k]: v });
  const defURL = PROVIDER_TYPES[form.type] ?? "";
  const isOAuth = OAUTH_TYPES.has(form.type);
  const pending = splitKeys(form.newKeys).length;
  const oauthType = OAUTH_TYPE_MAP[form.type];
  const typeHint = HINT_TYPES.has(form.type);
  const [login, setLogin] = useState(false);
  const consent = useSubscriptionConsent();
  const toast = useToast();

  const submit = async (f: Form = form, close = true) => {
    setSubmitted(true);
    if (Object.keys(validate(f, config, t)).length) return false;
    setBusy(true);
    setServerErr(undefined);
    const base = f.origId ? config.providers.find((p) => p.id === f.origId) || null : null;
    const prov = fromForm(f, base);
    const next = clone(config);
    if (f.origId) next.providers = next.providers.map((p) => (p.id === f.origId ? prov : p));
    else next.providers.push(prov);
    const r = await save(next);
    setBusy(false);
    if (r.ok && close) onClose();
    else if (r.ok) setForm({ ...f, origId: prov.id, keys: prov.keys, newKeys: "" });
    else setServerErr(r.error);
    return r.ok;
  };

  const onLoggedIn = async (accountId: string) => {
    setLogin(false);
    const f = { ...form, oauth: form.oauth.includes(accountId) ? form.oauth : [...form.oauth, accountId] };
    setForm(f);
    toast({ kind: "ok", title: t("oauth.done", { id: accountId }) });
    if (!Object.keys(validate(f, config, t)).length) await submit(f, false);
  };

  return (
    <Drawer
      open
      onClose={onClose}
      title={form.origId ? t("prov.editTitle", { id: form.origId }) : t("prov.add")}
      footer={
        <>
          {submitted && Object.keys(errs).length > 0 && (
            <span className="small" style={{ color: "var(--err)" }}>
              <Icon name="alert" size={12} /> {t("val.fixErrors", { n: Object.keys(errs).length })}
            </span>
          )}
          <span className="spacer" />
          <button className="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button className="btn primary" onClick={() => submit()} disabled={busy}>
            {busy ? t("common.saving") : t("common.save")}
          </button>
        </>
      }
    >
      <form
        className="stack"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {serverErr && <ErrorNote error={serverErr} />}
        <div className="grid-2">
          <Field label="ID" hint={t("prov.idHint")} error={show("id")}>
            {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!show("id")} className="input mono" value={form.id} onChange={(e) => set("id", e.target.value)} placeholder="anthropic-main" autoComplete="off" />}
          </Field>
          <Field label={t("prov.type")} error={show("type")}>
            {(id) => (
              <select id={id} className="input" value={form.type} onChange={(e) => set("type", e.target.value)}>
                {Object.keys(PROVIDER_TYPES).map((x) => (
                  <option key={x} value={x}>
                    {x}
                  </option>
                ))}
              </select>
            )}
          </Field>
        </div>
        <Field label={t("prov.baseUrl")} hint={typeHint ? t(`prov.urlHint.${form.type}` as Key) : defURL ? t("prov.baseUrlHint", { url: defURL }) : t("prov.baseUrlReq")} error={show("base_url")}>
          {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!show("base_url")} className="input mono" value={form.base_url} placeholder={defURL || "https://…/v1"} onChange={(e) => set("base_url", e.target.value)} />}
        </Field>

        {REGION_TYPES.has(form.type) && (
          <div className="grid-2">
            <Field label={t("prov.region")} hint={t(`prov.regionHint.${form.type}` as Key)} error={show("region")}>
              {(id, d) => (
                <input
                  id={id}
                  aria-describedby={d}
                  aria-invalid={!!show("region")}
                  className="input mono"
                  value={form.region}
                  placeholder={form.type === "bedrock" ? bedrockRegion(form.base_url) || "us-east-1" : "global"}
                  onChange={(e) => set("region", e.target.value)}
                />
              )}
            </Field>
            {form.type === "vertex" && (
              <Field label={t("prov.project")} hint={t("prov.projectHint")}>
                {(id, d) => <input id={id} aria-describedby={d} className="input mono" value={form.project} placeholder="my-gcp-project" onChange={(e) => set("project", e.target.value)} />}
              </Field>
            )}
          </div>
        )}

        <div className="section-title">{t("prov.creds")}</div>
        {errs.keys && <ErrorNote error={errs.keys} />}
        <div className="field">
          <span className="field-label">{t("prov.keys")}</span>
          {typeHint && <span className="hint">{t(`prov.keyHint.${form.type}` as Key)}</span>}
          {form.keys.length === 0 && <div className="faint small">{KEYLESS_TYPES.has(form.type) ? t("prov.keyless") : t("prov.noKeys")}</div>}
          {form.keys.map((k, i) => (
            <div className="list-row" key={`${k}-${i}`}>
              <Icon name="key" size={13} className="faint" />
              <span className="mono grow ellipsis">{isMasked(k) ? maskHint(k) : isKeyRef(k) ? k : `${k.slice(0, 6)}…${k.slice(-4)}`}</span>
              {isMasked(k) ? <Badge icon="lock">{t("prov.saved")}</Badge> : isKeyRef(k) ? <Badge tone="accent">{k.split(":")[0]}</Badge> : <Badge tone="warn">{t("prov.new")}</Badge>}
              <button type="button" className="btn ghost icon sm" aria-label={t("common.removeX", { x: `key ${i + 1}` })} onClick={() => set("keys", form.keys.filter((_, j) => j !== i))}>
                <Icon name="trash" size={13} />
              </button>
            </div>
          ))}
        </div>
        <Field label={t("prov.pasteKeys")} hint={t("prov.pasteHint")}>
          {(id, d) => (
            <div className="col">
              <textarea id={id} aria-describedby={d} className="input" rows={3} value={form.newKeys} onChange={(e) => set("newKeys", e.target.value)} placeholder={KEY_PLACEHOLDER[form.type] || "sk-...\nsk-...\nenv:MY_KEY"} spellCheck={false} autoComplete="off" />
              <div>
                <button type="button" className="btn sm" disabled={!pending} onClick={() => setForm({ ...form, keys: mergeKeys(form.keys, form.newKeys), newKeys: "" })}>
                  <Icon name="plus" size={13} /> {t("prov.addKeys", { n: pending })}
                </button>
              </div>
            </div>
          )}
        </Field>
        {(isOAuth || form.oauth.length > 0) && (
          <Field label={t("prov.oauth")} hint={t("prov.oauthHint")}>
            {(id, d) => <ChipsInput id={id} describedBy={d} values={form.oauth} onChange={(v) => set("oauth", v)} placeholder="account-id" />}
          </Field>
        )}
        {oauthType &&
          (login ? (
            <OAuthFlow oauthType={oauthType} onDone={onLoggedIn} onCancel={() => setLogin(false)} />
          ) : (
            <div>
              <button type="button" className="btn" onClick={async () => (await consent(oauthType)) && setLogin(true)}>
                <Icon name="lock" size={13} /> {t("oauth.loginWith", { x: OAUTH_LABEL[oauthType] || oauthType })}
              </button>
            </div>
          ))}

        <div className="section-title">{t("prov.modelsSec")}</div>
        <Field label={t("prov.models")} hint={t("prov.modelsHint")}>
          {(id, d) => <ChipsInput id={id} describedBy={d} values={form.models} onChange={(v) => set("models", v)} placeholder="claude-sonnet-4-5, gpt-5…" />}
        </Field>
        <div className="field">
          <span className="field-label">{t("prov.modelMap")}</span>
          <span className="hint">{t("prov.modelMapHint")}</span>
          <KVEditor rows={form.model_map} onChange={(r) => set("model_map", r)} keyPh={t("prov.clientModel")} valPh={t("prov.upstreamModel")} label={t("prov.modelMap")} addLabel={t("prov.addMapping")} />
          {show("model_map") && <div className="error">{show("model_map")}</div>}
        </div>

        <div className="section-title">{t("prov.advanced")}</div>
        <div className="grid-3">
          <Field label={t("prov.priority")} hint={t("prov.priorityHint")} error={show("priority")}>
            {(id, d) => <input id={id} aria-describedby={d} className="input num" inputMode="numeric" value={form.priority} placeholder="0" onChange={(e) => set("priority", e.target.value)} />}
          </Field>
          <Field label="first_byte" hint={t("prov.overrideHint")} error={show("first_byte")}>
            {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!show("first_byte")} className="input mono" value={form.first_byte} placeholder={fmtDuration(config.timeouts.first_byte)} onChange={(e) => set("first_byte", e.target.value)} />}
          </Field>
          <Field label="idle" hint={t("prov.overrideHint")} error={show("idle")}>
            {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!show("idle")} className="input mono" value={form.idle} placeholder={fmtDuration(config.timeouts.idle)} onChange={(e) => set("idle", e.target.value)} />}
          </Field>
        </div>
        <div className="grid-2">
          <Field label={t("prov.budget")} hint={t("prov.budgetHint")} error={show("budget_usd")}>
            {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!show("budget_usd")} className="input num" inputMode="decimal" value={form.budget_usd} placeholder="0" onChange={(e) => set("budget_usd", e.target.value)} />}
          </Field>
          <Field label={t("prov.budgetPeriod")}>
            {(id) => (
              <select id={id} className="input" value={form.budget_period} onChange={(e) => set("budget_period", e.target.value as BudgetPeriod)}>
                {BUDGET_PERIODS.map((x) => (
                  <option key={x} value={x}>
                    {t(`keys.perL.${x}` as Key)}
                  </option>
                ))}
              </select>
            )}
          </Field>
        </div>
        <Field label="Proxy" hint={t("prov.proxyHint")}>
          {(id, d) => <input id={id} aria-describedby={d} className="input mono" value={form.proxy} placeholder="http://127.0.0.1:7890" onChange={(e) => set("proxy", e.target.value)} />}
        </Field>
        <div className="field">
          <span className="field-label">{t("prov.headers")}</span>
          <KVEditor rows={form.headers} onChange={(r) => set("headers", r)} keyPh="Header" valPh={t("prov.value")} label={t("prov.headers")} addLabel={t("prov.addHeader")} />
          {show("headers") && <div className="error">{show("headers")}</div>}
        </div>
        <Switch checked={form.disabled} onChange={(v) => set("disabled", v)} label={t("prov.disableLabel")} />
        <button type="submit" hidden />
      </form>
    </Drawer>
  );
}
