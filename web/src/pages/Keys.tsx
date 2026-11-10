import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { CostBars, fillDays } from "../chart";
import type { Key } from "../dict";
import { ChipsInput } from "../editors";
import { useNow, usePoll, useStored } from "../hooks";
import { useT, type TFn } from "../i18n";
import { Icon } from "../icons";
import { useConfig, useStatus } from "../store";
import { BUDGET_PERIODS, type BudgetPeriod, type ClientKey, type Config, type KeyPatch, type KeyStatus, type KeyUsage } from "../types";
import { Badge, CopyButton, Drawer, Empty, ErrorNote, Field, Meter, Switch, useConfirm, useToast, type Tone } from "../ui";
import { endOfDayISO, fmtAgo, fmtNum, fmtUsd, isLoopbackListen, isMasked, maskHint, publicBase, toDateInput } from "../util";

/* ---------- helpers ---------- */

const STATUS_TONE: Record<KeyStatus, Tone> = { active: "ok", disabled: "neutral", expired: "warn", over_budget: "err" };

function keyStatus(k: ClientKey, u: KeyUsage | undefined, now: number): KeyStatus {
  if (u) return u.status;
  if (k.disabled) return "disabled";
  if (k.expires_at && new Date(k.expires_at).getTime() < now) return "expired";
  return "active";
}

function periodOf(k: ClientKey): BudgetPeriod {
  return k.budget_period || "monthly";
}

/** Short, human summary of a key's limits (used in the table and share message). */
export function limitsSummary(k: ClientKey, t: TFn): string[] {
  const out: string[] = [];
  const p = t(`keys.per.${periodOf(k)}` as Key);
  if (k.budget_usd) out.push(t("keys.msg.budget", { v: fmtUsd(k.budget_usd), p }));
  if (k.budget_tokens) out.push(t("keys.msg.tokens", { v: fmtNum(k.budget_tokens), p }));
  if (k.rpm) out.push(t("keys.rpm", { n: k.rpm }));
  if (k.tpm) out.push(t("keys.tpm", { n: fmtNum(k.tpm) }));
  if (k.max_parallel) out.push(t("keys.parallel", { n: k.max_parallel }));
  if (k.expires_at) out.push(t("keys.msg.expires", { d: toDateInput(k.expires_at) }));
  return out;
}

/** Picks Claude Code model names for a key: a concrete allowed model, else the first routable one. */
function pickModels(k: ClientKey, routable: string[]): { main: string; small: string } {
  const allowed = (k.models || []).filter((m) => !m.includes("*"));
  const pool = allowed.length ? allowed : routable;
  const main = pool[0] || "<model>";
  const small = pool.find((m) => /haiku|mini|flash|small|lite/i.test(m)) || main;
  return { main, small };
}

export function shareBlocks(k: ClientKey, keyValue: string, base: string, routable: string[], t: TFn) {
  const { main, small } = pickModels(k, routable);
  const unix = [
    `export ANTHROPIC_BASE_URL=${base}`,
    `export ANTHROPIC_AUTH_TOKEN=${keyValue}`,
    `export ANTHROPIC_MODEL=${main}`,
    `export ANTHROPIC_DEFAULT_OPUS_MODEL=${main}`,
    `export ANTHROPIC_DEFAULT_SONNET_MODEL=${main}`,
    `export ANTHROPIC_DEFAULT_HAIKU_MODEL=${small}`,
    "claude",
  ].join("\n");
  const win = [
    `$env:ANTHROPIC_BASE_URL = "${base}"`,
    `$env:ANTHROPIC_AUTH_TOKEN = "${keyValue}"`,
    `$env:ANTHROPIC_MODEL = "${main}"`,
    `$env:ANTHROPIC_DEFAULT_OPUS_MODEL = "${main}"`,
    `$env:ANTHROPIC_DEFAULT_SONNET_MODEL = "${main}"`,
    `$env:ANTHROPIC_DEFAULT_HAIKU_MODEL = "${small}"`,
    "claude",
  ].join("\n");
  const lim = limitsSummary(k, t);
  const head = [
    t("keys.msg.hello", { name: k.name }),
    "",
    `${t("keys.msg.endpoint")}: ${base}`,
    `${t("keys.msg.openai")}: ${base}/v1`,
    `${t("keys.msg.anthropic")}: ${base}`,
    `${t("keys.msg.key")}: ${keyValue}`,
    `${t("keys.msg.models")}: ${k.models && k.models.length ? k.models.join(", ") : t("keys.allModels")}`,
    `${t("keys.msg.limits")}: ${lim.length ? lim.join(" · ") : t("keys.noLimit")}`,
    `${t("keys.msg.usage")}: ${base}/me`,
  ].join("\n");
  const message = `${head}\n\n${t("keys.msg.claude")}:\n${unix}\n\n${t("keys.msg.claudeWin")}:\n${win}\n`;
  return { head, unix, win, message };
}

/* ---------- form ---------- */

interface Form {
  origName: string | null;
  name: string;
  note: string;
  models: string[];
  budget_usd: string;
  budget_tokens: string;
  budget_period: BudgetPeriod;
  rpm: string;
  tpm: string;
  max_parallel: string;
  expires: string;
  disabled: boolean;
}

function toForm(k: ClientKey | null, n: number): Form {
  return {
    origName: k ? k.name : null,
    name: k?.name || `user-${n + 1}`,
    note: k?.note || "",
    models: k?.models ? [...k.models] : [],
    budget_usd: k?.budget_usd ? String(k.budget_usd) : "",
    budget_tokens: k?.budget_tokens ? String(k.budget_tokens) : "",
    budget_period: k?.budget_period || "monthly",
    rpm: k?.rpm ? String(k.rpm) : "",
    tpm: k?.tpm ? String(k.tpm) : "",
    max_parallel: k?.max_parallel ? String(k.max_parallel) : "",
    expires: toDateInput(k?.expires_at),
    disabled: !!k?.disabled,
  };
}

const num = (s: string) => (s.trim() === "" ? 0 : Number(s));
const badNum = (s: string) => s.trim() !== "" && !(Number.isFinite(Number(s)) && Number(s) >= 0);

type Errors = Partial<Record<"name" | "budget_usd" | "budget_tokens" | "rpm" | "tpm" | "max_parallel", string>>;

function validate(f: Form, cfg: Config, t: TFn): Errors {
  const e: Errors = {};
  const n = f.name.trim();
  if (!n) e.name = t("val.required");
  else if (n !== f.origName && cfg.api_keys.some((k) => k.name === n)) e.name = t("val.kvDup", { k: n });
  for (const k of ["budget_usd", "budget_tokens", "rpm", "tpm", "max_parallel"] as const) if (badNum(f[k])) e[k] = t("val.nonNeg");
  return e;
}

function toPatch(f: Form): KeyPatch {
  return {
    name: f.name.trim(),
    note: f.note.trim(),
    models: f.models,
    budget_usd: num(f.budget_usd),
    budget_tokens: Math.round(num(f.budget_tokens)),
    budget_period: f.budget_period,
    rpm: Math.round(num(f.rpm)),
    tpm: Math.round(num(f.tpm)),
    max_parallel: Math.round(num(f.max_parallel)),
    expires_at: endOfDayISO(f.expires),
    disabled: f.disabled,
  };
}

/* ---------- page ---------- */

interface ShareState {
  name: string;
  key?: string; // plaintext, only right after create/rotate
}

export default function Keys() {
  const t = useT();
  const confirm = useConfirm();
  const { config, error, loading, reload, models, reloadModels } = useConfig();
  const toast = useToast();
  const { status } = useStatus();
  const usage = usePoll((s) => api.keysUsage(s), 5000);
  const month = usePoll((s) => api.usage({ range: "30d", group: "key" }, s), 30000);
  const now = useNow(5000);
  const [editing, setEditing] = useState<Form | null>(null);
  const [share, setShare] = useState<ShareState | null>(null);

  const byName = useMemo(() => new Map((usage.data || []).map((u) => [u.name, u])), [usage.data]);

  if (loading && !config) return <div className="faint">{t("common.loading")}</div>;
  if (!config) return <ErrorNote error={error} />;

  const keys = config.api_keys;
  const active = keys.filter((k) => keyStatus(k, byName.get(k.name), now) === "active").length;
  const tokensPeriod = (usage.data || []).reduce((a, u) => a + u.input_tokens + u.output_tokens, 0);
  const reqToday = (usage.data || []).reduce((a, u) => a + (u.today?.requests || 0), 0);

  // Each action uses the key management API (one key at a time), so it
  // cannot overwrite unrelated edits made elsewhere.
  const run = async <T,>(fn: () => Promise<T>, okTitle: string): Promise<{ ok: boolean; error?: string; value?: T }> => {
    try {
      const value = await fn();
      await reload();
      reloadModels();
      usage.refresh();
      toast({ kind: "ok", title: okTitle });
      return { ok: true, value };
    } catch (e) {
      const msg = (e as Error).message;
      toast({ kind: "err", title: t("save.fail"), body: msg });
      return { ok: false, error: msg };
    }
  };

  const toggle = (k: ClientKey) => run(() => api.updateKey(k.name, { disabled: !k.disabled }), k.disabled ? t("keys.enabledToast", { name: k.name }) : t("keys.disabledToast", { name: k.name }));

  const remove = async (k: ClientKey) => {
    if (!(await confirm({ title: t("keys.deleteTitle", { name: k.name }), body: t("keys.deleteBody"), confirm: t("common.delete"), danger: true }))) return;
    await run(() => api.deleteKey(k.name), t("keys.deletedToast", { name: k.name }));
  };

  const rotate = async (k: ClientKey) => {
    if (!(await confirm({ title: t("keys.rotateTitle", { name: k.name }), body: t("keys.rotateBody"), confirm: t("keys.rotate") }))) return;
    const r = await run(() => api.rotateKey(k.name), t("keys.rotatedToast", { name: k.name }));
    if (r.ok && r.value?.key) setShare({ name: k.name, key: r.value.key });
  };

  return (
    <div className="stack-lg">
      <div className="page-head" style={{ marginBottom: 0 }}>
        <div className="grow">
          <h1>{t("nav.keys")}</h1>
          <div className="desc">{t("keys.desc")}</div>
        </div>
        <button className="btn primary" onClick={() => setEditing(toForm(null, keys.length))}>
          <Icon name="plus" size={14} /> {t("keys.create")}
        </button>
      </div>

      <div className="mini-stats">
        <MiniStat icon="key" label={t("keys.active")} value={`${active} / ${keys.length}`} />
        <MiniStat icon="bolt" label={t("keys.cost30")} value={month.data ? fmtUsd(month.data.total.cost_usd) : "–"} />
        <MiniStat icon="down" label={t("keys.tokensPeriod")} value={usage.data ? fmtNum(tokensPeriod) : "–"} />
        <MiniStat icon="requests" label={t("keys.reqToday")} value={usage.data ? fmtNum(reqToday) : "–"} />
      </div>

      {usage.error && !usage.data && <ErrorNote error={usage.error} />}

      {keys.length === 0 ? (
        <div className="card">
          <Empty
            icon="key"
            title={t("keys.emptyTitle")}
            body={t("keys.emptyBody")}
            action={
              <button className="btn primary" onClick={() => setEditing(toForm(null, 0))}>
                <Icon name="plus" size={14} /> {t("keys.create")}
              </button>
            }
          />
        </div>
      ) : (
        <div className="card">
          <div className="table-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>{t("keys.colKey")}</th>
                  <th>{t("keys.colModels")}</th>
                  <th>{t("keys.colUsage")}</th>
                  <th className="hide-sm">{t("keys.colLimits")}</th>
                  <th className="hide-sm">{t("keys.colLastUsed")}</th>
                  <th className="hide-sm">{t("keys.colExpires")}</th>
                  <th className="r">
                    <span className="sr-only">{t("common.actions")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {keys.map((k) => {
                  const u = byName.get(k.name);
                  const st = keyStatus(k, u, now);
                  return (
                    <tr key={k.name} className={st === "disabled" ? "dim" : undefined}>
                      <td style={{ maxWidth: 260 }}>
                        <div className="row" style={{ gap: 8 }}>
                          <span className="key-name ellipsis" title={k.name}>
                            {k.name}
                          </span>
                          <Badge tone={STATUS_TONE[st]}>{t(`keys.st.${st}` as Key)}</Badge>
                        </div>
                        <div className="key-note ellipsis" title={k.note}>
                          {k.note || <span className="mono">{isMasked(k.key) ? maskHint(k.key) : k.key.startsWith("env:") ? k.key : "…"}</span>}
                        </div>
                      </td>
                      <td style={{ maxWidth: 220 }}>
                        {k.models && k.models.length ? (
                          <div className="row wrap" style={{ gap: 4 }}>
                            {k.models.slice(0, 3).map((m) => (
                              <span key={m} className="chip static">
                                {m}
                              </span>
                            ))}
                            {k.models.length > 3 && <span className="faint small">+{k.models.length - 3}</span>}
                          </div>
                        ) : (
                          <span className="faint small">{t("keys.allModels")}</span>
                        )}
                      </td>
                      <td className="usage-cell">
                        <UsageCell k={k} u={u} />
                      </td>
                      <td className="hide-sm small nowrap">
                        {k.rpm || k.tpm || k.max_parallel ? (
                          <div className="col" style={{ gap: 2 }}>
                            {k.rpm ? <span>{t("keys.rpm", { n: k.rpm })}</span> : null}
                            {k.tpm ? <span>{t("keys.tpm", { n: fmtNum(k.tpm) })}</span> : null}
                            {k.max_parallel ? <span className="faint">{t("keys.parallel", { n: k.max_parallel })}</span> : null}
                          </div>
                        ) : (
                          <span className="faint">{t("keys.noLimit")}</span>
                        )}
                        {u && u.inflight > 0 && <div className="faint">{t("keys.inflight", { n: u.inflight })}</div>}
                      </td>
                      <td className="hide-sm small nowrap faint">{u?.last_used ? t("keys.ago", { t: fmtAgo(u.last_used, now) }) : "–"}</td>
                      <td className="hide-sm small nowrap">{k.expires_at ? toDateInput(k.expires_at) : <span className="faint">{t("keys.never")}</span>}</td>
                      <td className="r">
                        <div className="key-actions" role="group" aria-label={t("keys.actionsFor", { name: k.name })}>
                          <button className="btn sm" onClick={() => setShare({ name: k.name })}>
                            <Icon name="send" size={13} /> {t("keys.share")}
                          </button>
                          <button className="btn ghost icon sm" title={t("common.edit")} aria-label={`${t("common.edit")} ${k.name}`} onClick={() => setEditing(toForm(k, 0))}>
                            <Icon name="edit" size={14} />
                          </button>
                          <button className="btn ghost icon sm" title={t("keys.rotate")} aria-label={`${t("keys.rotate")} ${k.name}`} onClick={() => rotate(k)}>
                            <Icon name="refresh" size={14} />
                          </button>
                          <button
                            className="btn ghost icon sm"
                            title={k.disabled ? t("keys.enable") : t("keys.disable")}
                            aria-label={`${k.disabled ? t("keys.enable") : t("keys.disable")} ${k.name}`}
                            onClick={() => toggle(k)}
                          >
                            <Icon name={k.disabled ? "play" : "pause"} size={14} />
                          </button>
                          <button className="btn ghost icon sm danger" title={t("common.delete")} aria-label={`${t("common.delete")} ${k.name}`} onClick={() => remove(k)}>
                            <Icon name="trash" size={14} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <UsageSection />

      {editing && (
        <KeyDrawer
          form={editing}
          setForm={setEditing}
          config={config}
          suggestions={models.map((m) => m.id)}
          onClose={() => setEditing(null)}
          onSave={async (f) => {
            const patch = toPatch(f);
            const r = f.origName
              ? await run(() => api.updateKey(f.origName!, patch), t("save.ok"))
              : await run(() => api.createKey(patch), t("keys.createdToast", { name: patch.name || "" }));
            if (r.ok) {
              setEditing(null);
              if (!f.origName && r.value?.key) setShare({ name: r.value.name, key: r.value.key });
            }
            return { ok: r.ok, error: r.error };
          }}
        />
      )}
      {share && (
        <ShareDrawer
          state={share}
          config={config}
          listen={status?.listen || config.listen}
          routable={models.map((m) => m.id)}
          onClose={() => setShare(null)}
          onRotate={(k) => {
            setShare(null);
            rotate(k);
          }}
        />
      )}
    </div>
  );
}

function MiniStat({ icon, label, value }: { icon: "key" | "bolt" | "down" | "requests"; label: string; value: string }) {
  return (
    <div className="mini-stat">
      <Icon name={icon} size={16} />
      <div className="grow">
        <div className="ms-label">{label}</div>
        <div className="ms-value">{value}</div>
      </div>
    </div>
  );
}

function UsageCell({ k, u }: { k: ClientKey; u?: KeyUsage }) {
  const t = useT();
  if (!u) return <span className="faint small">–</span>;
  const p = t(`keys.per.${u.period || periodOf(k)}` as Key);
  const tokens = u.input_tokens + u.output_tokens;
  const blocks = [];
  if (k.budget_usd) {
    blocks.push(<Meter key="usd" pct={(u.cost_usd / k.budget_usd) * 100} value={`${fmtUsd(u.cost_usd)} / ${fmtUsd(k.budget_usd)}`} sub={p} label={t("keys.budgetUsd")} />);
  }
  if (k.budget_tokens) {
    blocks.push(
      <Meter key="tok" pct={(tokens / k.budget_tokens) * 100} value={`${fmtNum(tokens)} / ${fmtNum(k.budget_tokens)}`} sub={k.budget_usd ? t("keys.tokensUnit") : `${t("keys.tokensUnit")} · ${p}`} label={t("keys.budgetTokens")} />,
    );
  }
  return (
    <div>
      {blocks.length ? (
        blocks
      ) : (
        <div className="small">
          <strong>{fmtUsd(u.cost_usd)}</strong> <span className="faint">· {fmtNum(tokens)} {t("keys.tokensUnit")} · {p}</span>
        </div>
      )}
      <div className="faint small" style={{ marginTop: 4 }}>
        {fmtNum(u.requests)} {t("keys.reqUnit")} · {t("keys.today")} {fmtUsd(u.today?.cost_usd)}
      </div>
    </div>
  );
}

/* ---------- create / edit ---------- */

function KeyDrawer({
  form,
  setForm,
  config,
  suggestions,
  onClose,
  onSave,
}: {
  form: Form;
  setForm: (f: Form) => void;
  config: Config;
  suggestions: string[];
  onClose: () => void;
  onSave: (f: Form) => Promise<{ ok: boolean; error?: string }>;
}) {
  const t = useT();
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [serverErr, setServerErr] = useState<string>();
  const errs = validate(form, config, t);
  const show = (k: keyof Errors) => (submitted ? errs[k] : undefined);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm({ ...form, [k]: v });
  const submit = async () => {
    setSubmitted(true);
    if (Object.keys(errs).length) return;
    setBusy(true);
    setServerErr(undefined);
    const r = await onSave(form);
    setBusy(false);
    if (!r.ok) setServerErr(r.error);
  };
  const numInput = (k: "budget_usd" | "budget_tokens" | "rpm" | "tpm" | "max_parallel", label: string, hint: string, ph = "0") => (
    <Field label={label} hint={hint} error={show(k)}>
      {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!show(k)} className="input num" inputMode="decimal" value={form[k]} placeholder={ph} onChange={(e) => set(k, e.target.value)} />}
    </Field>
  );
  return (
    <Drawer
      open
      onClose={onClose}
      title={form.origName ? t("keys.editTitle", { name: form.origName }) : t("keys.newTitle")}
      footer={
        <>
          <span className="spacer" />
          <button className="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button className="btn primary" onClick={submit} disabled={busy}>
            {busy ? t("common.saving") : form.origName ? t("common.save") : t("keys.create")}
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
          <Field label={t("keys.name")} hint={t("keys.nameHint")} error={show("name")}>
            {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!show("name")} className="input mono" value={form.name} onChange={(e) => set("name", e.target.value)} autoComplete="off" />}
          </Field>
          <Field label={t("keys.note")}>
            {(id) => <input id={id} className="input" value={form.note} placeholder={t("keys.notePh")} onChange={(e) => set("note", e.target.value)} />}
          </Field>
        </div>
        <Field label={t("keys.models")} hint={t("keys.modelsHint")}>
          {(id, d) => <ChipsInput id={id} describedBy={d} values={form.models} onChange={(v) => set("models", v)} placeholder="coding, claude-*" suggestions={suggestions} />}
        </Field>

        <div className="section-title">{t("keys.budgetSec")}</div>
        <div className="grid-3">
          {numInput("budget_usd", t("keys.budgetUsd"), t("keys.budgetUsdHint"))}
          {numInput("budget_tokens", t("keys.budgetTokens"), t("keys.budgetTokensHint"))}
          <Field label={t("keys.period")} hint={t("keys.periodHint")}>
            {(id, d) => (
              <select id={id} aria-describedby={d} className="input" value={form.budget_period} onChange={(e) => set("budget_period", e.target.value as BudgetPeriod)}>
                {BUDGET_PERIODS.map((p) => (
                  <option key={p} value={p}>
                    {t(`keys.perL.${p}` as Key)}
                  </option>
                ))}
              </select>
            )}
          </Field>
        </div>

        <div className="section-title">{t("keys.rateSec")}</div>
        <div className="grid-3">
          {numInput("rpm", t("keys.rpmLabel"), t("keys.zeroUnlimited"))}
          {numInput("tpm", t("keys.tpmLabel"), t("keys.tpmHint"))}
          {numInput("max_parallel", t("keys.parallelLabel"), t("keys.zeroUnlimited"))}
          <Field label={t("keys.expires")} hint={t("keys.expiresHint")}>
            {(id, d) => (
              <div className="row" style={{ gap: 4 }}>
                <input id={id} aria-describedby={d} className="input" type="date" value={form.expires} onChange={(e) => set("expires", e.target.value)} />
                {form.expires && (
                  <button type="button" className="btn ghost icon sm" aria-label={t("keys.clearDate")} title={t("keys.clearDate")} onClick={() => set("expires", "")}>
                    <Icon name="x" size={14} />
                  </button>
                )}
              </div>
            )}
          </Field>
        </div>
        <Switch checked={form.disabled} onChange={(v) => set("disabled", v)} label={t("keys.disabledLabel")} />
        <button type="submit" hidden />
      </form>
    </Drawer>
  );
}

/* ---------- share ---------- */

function ShareDrawer({ state, config, listen, routable, onClose, onRotate }: { state: ShareState; config: Config; listen: string; routable: string[]; onClose: () => void; onRotate: (k: ClientKey) => void }) {
  const t = useT();
  const k = config.api_keys.find((x) => x.name === state.name) || ({ name: state.name, key: "" } as ClientKey);
  const base = publicBase(config.public_url, listen);
  const keyValue = state.key || "<YOUR_KEY>";
  const b = shareBlocks(k, keyValue, base, routable, t);
  const unreachable = !config.public_url && isLoopbackListen(listen);
  return (
    <Drawer
      open
      onClose={onClose}
      width={720}
      title={state.key ? t("keys.newKeyTitle", { name: k.name }) : t("keys.shareTitle", { name: k.name })}
      footer={
        <>
          {!state.key && (
            <button className="btn" onClick={() => onRotate(k)}>
              <Icon name="refresh" size={13} /> {t("keys.rotate")}
            </button>
          )}
          <span className="spacer" />
          <button className="btn" onClick={onClose}>
            {t("common.close")}
          </button>
          <CopyButton text={b.message} label={t("keys.copyMessage")} className="btn primary" />
        </>
      }
    >
      <div className="stack">
        {unreachable && (
          <div className="alert warn" role="alert">
            <Icon name="alert" style={{ flex: "none", marginTop: 2 }} />
            <div>
              {t("keys.loopbackWarn", { listen })}{" "}
              <Link to="/settings" onClick={onClose}>
                {t("keys.goSettings")} →
              </Link>
            </div>
          </div>
        )}
        {state.key ? (
          <>
            <div className="alert info">
              <Icon name="lock" style={{ flex: "none", marginTop: 2 }} />
              <div>{t("keys.newKeyWarn")}</div>
            </div>
            <div className="secret-box">
              <code>{state.key}</code>
              <CopyButton text={state.key} className="btn sm" />
            </div>
          </>
        ) : (
          <p className="muted small">{t("keys.keyHidden")}</p>
        )}
        <Snippet title={t("keys.message")} code={b.head} />
        <Snippet title={t("keys.msg.claude")} code={b.unix} />
        <Snippet title={t("keys.msg.claudeWin")} code={b.win} />
      </div>
    </Drawer>
  );
}

function Snippet({ title, code }: { title: string; code: string }) {
  return (
    <div className="snippet">
      <div className="snippet-head">
        <span className="s-title grow ellipsis">{title}</span>
        <CopyButton text={code} className="btn xs" />
      </div>
      <pre className="code" tabIndex={0} aria-label={title}>
        {code}
      </pre>
    </div>
  );
}

/* ---------- usage report ---------- */

const RANGES = ["7d", "30d", "90d"] as const;
const GROUPS = ["key", "model", "provider"] as const;

function UsageSection() {
  const t = useT();
  const [range, setRange] = useStored<(typeof RANGES)[number]>("clr.usageRange", "30d");
  const [group, setGroup] = useStored<(typeof GROUPS)[number]>("clr.usageGroup", "key");
  const rep = usePoll((s) => api.usage({ range, group }, s), 30000, [range, group]);
  const d = rep.data;
  const days = useMemo(() => (d ? fillDays(d.daily, d.since) : []), [d]);
  return (
    <section className="card" aria-labelledby="usage-h">
      <div className="card-head" style={{ flexWrap: "wrap" }}>
        <h2 id="usage-h" className="grow">
          {t("keys.usageTitle")}
        </h2>
        <div className="seg" role="group" aria-label={t("keys.usageTitle")}>
          {GROUPS.map((g) => (
            <button key={g} aria-pressed={group === g} onClick={() => setGroup(g)}>
              {t(`keys.grp.${g}` as Key)}
            </button>
          ))}
        </div>
        <div className="seg" role="group" aria-label={t("ov.range")}>
          {RANGES.map((r) => (
            <button key={r} aria-pressed={range === r} onClick={() => setRange(r)}>
              {r}
            </button>
          ))}
        </div>
      </div>
      <div className="card-body">
        {rep.error && !d && <ErrorNote error={rep.error} />}
        <div className="row" style={{ marginBottom: 8 }}>
          <h3 className="grow">{t("keys.dailyCost")}</h3>
          {d && <strong className="num">{fmtUsd(d.total.cost_usd)}</strong>}
        </div>
        <CostBars data={days} label={t("keys.dailyCost")} />
        <p className="faint small" style={{ marginTop: 8 }}>
          {t("keys.costNote")}
        </p>
      </div>
      <div className="table-wrap">
        <table className="tbl">
          <thead>
            <tr>
              <th>{t("keys.colName")}</th>
              <th className="r">{t("keys.colReq")}</th>
              <th className="r">{t("keys.colErr")}</th>
              <th className="r">{t("keys.colTokIn")}</th>
              <th className="r">{t("keys.colTokOut")}</th>
              <th className="r hide-sm">{t("keys.colCache")}</th>
              <th className="r">{t("keys.colCost")}</th>
            </tr>
          </thead>
          <tbody>
            {!d && (
              <tr>
                <td colSpan={7}>
                  <span className="skeleton">loading</span>
                </td>
              </tr>
            )}
            {d && d.rows.length === 0 && (
              <tr>
                <td colSpan={7} className="faint">
                  {t("keys.noUsage")}
                </td>
              </tr>
            )}
            {d?.rows.map((r) => (
              <tr key={r.key}>
                <td className="mono ellipsis" style={{ maxWidth: 280 }} title={r.key}>
                  {r.key || "—"}
                </td>
                <td className="r">{fmtNum(r.requests)}</td>
                <td className="r">{r.errors ? <Badge tone="err">{fmtNum(r.errors)}</Badge> : <span className="faint">0</span>}</td>
                <td className="r">{fmtNum(r.input_tokens)}</td>
                <td className="r">{fmtNum(r.output_tokens)}</td>
                <td className="r hide-sm">{fmtNum(r.cache_read_tokens)}</td>
                <td className="r">
                  <strong>{fmtUsd(r.cost_usd)}</strong>
                </td>
              </tr>
            ))}
            {d && d.rows.length > 1 && (
              <tr>
                <td>
                  <strong>{t("keys.total")}</strong>
                </td>
                <td className="r">{fmtNum(d.total.requests)}</td>
                <td className="r">{fmtNum(d.total.errors)}</td>
                <td className="r">{fmtNum(d.total.input_tokens)}</td>
                <td className="r">{fmtNum(d.total.output_tokens)}</td>
                <td className="r hide-sm">{fmtNum(d.total.cache_read_tokens)}</td>
                <td className="r">
                  <strong>{fmtUsd(d.total.cost_usd)}</strong>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}
