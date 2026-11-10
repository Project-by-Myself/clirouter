import { useEffect, useState } from "react";
import { setPassword } from "../api";
import type { Key } from "../dict";
import { useT, type TFn } from "../i18n";
import { Icon } from "../icons";
import { diffConfig, useConfig } from "../store";
import type { ClientKey, Config, Logging, Routing, Timeouts } from "../types";
import { Badge, CopyButton, ErrorNote, Field, Switch, useConfirm } from "../ui";
import { clone, fmtDuration, isMasked, maskHint, parseDuration, randomKey } from "../util";

const TIMEOUTS: (keyof Timeouts)[] = ["connect", "first_byte", "idle", "total", "early_ping", "keepalive"];

interface Draft {
  listen: string;
  api_keys: ClientKey[];
  admin_password: string;
  timeouts: Record<keyof Timeouts, string>;
  routing: { strategy: string; max_attempts: string; session_affinity: boolean; max_body_mb: string };
  logging: { level: string; retention_days: string; capture_bodies: boolean };
}

function toDraft(c: Config): Draft {
  const to = {} as Record<keyof Timeouts, string>;
  for (const k of TIMEOUTS) to[k] = fmtDuration(c.timeouts[k]) || "0s";
  return {
    listen: c.listen,
    api_keys: clone(c.api_keys),
    admin_password: c.admin_password || "",
    timeouts: to,
    routing: { strategy: c.routing.strategy, max_attempts: String(c.routing.max_attempts), session_affinity: c.routing.session_affinity, max_body_mb: String(c.routing.max_body_mb) },
    logging: { level: c.logging.level || "info", retention_days: String(c.logging.retention_days), capture_bodies: c.logging.capture_bodies },
  };
}

const intOk = (s: string) => /^\d+$/.test(s.trim());

function validate(d: Draft, t: TFn): Record<string, string> {
  const e: Record<string, string> = {};
  if (!d.listen.trim()) e.listen = t("val.required");
  else if (!/:\d+$/.test(d.listen.trim())) e.listen = t("set.listenFormat");
  for (const k of TIMEOUTS) if (parseDuration(d.timeouts[k]) === null) e[`t.${k}`] = t("val.duration");
  if (!intOk(d.routing.max_attempts)) e.max_attempts = t("val.int");
  if (!intOk(d.routing.max_body_mb)) e.max_body_mb = t("val.int");
  if (!intOk(d.logging.retention_days)) e.retention_days = t("val.int");
  const names = new Set<string>();
  const keys = new Set<string>();
  d.api_keys.forEach((k, i) => {
    if (!k.name.trim()) e[`k.${i}`] = t("set.keyNameReq");
    else if (names.has(k.name.trim())) e[`k.${i}`] = t("val.kvDup", { k: k.name });
    else if (!k.key.trim()) e[`k.${i}`] = t("set.keyReq");
    else if (keys.has(k.key)) e[`k.${i}`] = t("set.keyDup");
    names.add(k.name.trim());
    keys.add(k.key);
  });
  return e;
}

function apply(c: Config, d: Draft): Config {
  const n = clone(c);
  n.listen = d.listen.trim();
  n.api_keys = d.api_keys.map((k) => ({ name: k.name.trim(), key: k.key.trim() }));
  n.admin_password = d.admin_password;
  for (const k of TIMEOUTS) n.timeouts[k] = parseDuration(d.timeouts[k]) ?? c.timeouts[k];
  const r: Routing = { strategy: d.routing.strategy, max_attempts: parseInt(d.routing.max_attempts, 10) || 0, session_affinity: d.routing.session_affinity, max_body_mb: parseInt(d.routing.max_body_mb, 10) || 0 };
  const l: Logging = { level: d.logging.level, retention_days: parseInt(d.logging.retention_days, 10) || 0, capture_bodies: d.logging.capture_bodies };
  n.routing = r;
  n.logging = l;
  return n;
}

export default function Settings() {
  const t = useT();
  const confirm = useConfirm();
  const { config, error, save } = useConfig();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [busy, setBusy] = useState(false);
  const [serverErr, setServerErr] = useState<string>();
  const [newPw, setNewPw] = useState("");

  useEffect(() => {
    if (config && !draft) setDraft(toDraft(config));
  }, [config, draft]);

  if (!config || !draft) return error ? <ErrorNote error={error} /> : <div className="faint">{t("common.loading")}</div>;

  const next = apply(config, draft);
  const changes = diffConfig(config, next, t);
  const errs = validate(draft, t);
  const nErr = Object.keys(errs).length;
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft({ ...draft, [k]: v });
  const setKey = (i: number, p: Partial<ClientKey>) => set("api_keys", draft.api_keys.map((k, j) => (j === i ? { ...k, ...p } : k)));

  const submit = async () => {
    if (nErr) return;
    if (config.listen !== next.listen && !(await confirm({ title: t("set.listenConfirm"), body: t("set.listenNote"), confirm: t("common.save") }))) return;
    if (config.admin_password && !next.admin_password && !(await confirm({ title: t("set.pwRemoveTitle"), body: t("set.pwRemoveBody"), confirm: t("common.save"), danger: true }))) return;
    setBusy(true);
    setServerErr(undefined);
    const r = await save(next);
    setBusy(false);
    if (r.ok) {
      const pw = next.admin_password || "";
      if (!isMasked(pw) && !pw.startsWith("env:")) setPassword(pw);
      setNewPw("");
      setDraft(null);
    } else setServerErr(r.error);
  };

  const timeoutHelp: Record<keyof Timeouts, Key> = {
    connect: "set.t.connect",
    first_byte: "set.t.first_byte",
    idle: "set.t.idle",
    total: "set.t.total",
    early_ping: "set.t.early_ping",
    keepalive: "set.t.keepalive",
  };

  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.settings")}</h1>
          <div className="desc">{t("set.desc")}</div>
        </div>
      </div>
      <div className="stack-lg">
        <section className="card">
          <div className="card-head">
            <h2>{t("set.server")}</h2>
          </div>
          <div className="card-body grid-2">
            <Field label="listen" hint={t("set.listenHint")} error={errs.listen}>
              {(id, d) => <input id={id} aria-describedby={d} aria-invalid={!!errs.listen} className="input mono" value={draft.listen} onChange={(e) => set("listen", e.target.value)} />}
            </Field>
            <div className="field">
              <span className="field-label">data_dir</span>
              <div className="input mono" style={{ display: "flex", alignItems: "center", background: "var(--surface-2)" }} aria-readonly="true">
                <span className="ellipsis">{config.data_dir || "–"}</span>
              </div>
              <span className="hint">{t("set.dataDirHint")}</span>
            </div>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2 className="grow">{t("set.clientKeys")}</h2>
            <button
              className="btn sm"
              onClick={() => set("api_keys", [...draft.api_keys, { name: `key-${draft.api_keys.length + 1}`, key: randomKey() }])}
            >
              <Icon name="plus" size={13} /> {t("set.addKey")}
            </button>
          </div>
          <div className="card-body">
            <p className="muted small" style={{ marginBottom: 10 }}>
              {draft.api_keys.length === 0 ? t("set.noKeys") : t("set.keysHint")}
            </p>
            {draft.api_keys.map((k, i) => (
              <div key={i} style={{ marginBottom: 8 }}>
                <div className="row wrap" style={{ alignItems: "center" }}>
                  <input className="input" style={{ width: 160 }} aria-label={t("set.keyName", { i: i + 1 })} value={k.name} onChange={(e) => setKey(i, { name: e.target.value })} placeholder="name" />
                  {isMasked(k.key) ? (
                    <div className="row grow" style={{ minWidth: 180 }}>
                      <span className="mono ellipsis">{maskHint(k.key)}</span>
                      <Badge icon="lock">{t("prov.saved")}</Badge>
                      <button className="btn ghost sm" onClick={() => setKey(i, { key: randomKey() })}>
                        <Icon name="dice" size={13} /> {t("set.rotate")}
                      </button>
                    </div>
                  ) : (
                    <>
                      <input className="input mono grow" style={{ minWidth: 180, flex: 1, width: "auto" }} aria-label={t("set.keyValue", { i: i + 1 })} value={k.key} onChange={(e) => setKey(i, { key: e.target.value })} placeholder="sk-clr-… / env:NAME" />
                      <button className="btn ghost icon sm" onClick={() => setKey(i, { key: randomKey() })} aria-label={t("set.generate")} title={t("set.generate")}>
                        <Icon name="dice" size={14} />
                      </button>
                      <CopyButton text={k.key} />
                    </>
                  )}
                  <button className="btn ghost icon sm" onClick={() => set("api_keys", draft.api_keys.filter((_, j) => j !== i))} aria-label={t("common.removeX", { x: k.name })}>
                    <Icon name="trash" size={14} />
                  </button>
                </div>
                {errs[`k.${i}`] && (
                  <div className="small" style={{ color: "var(--err)", marginTop: 4 }}>
                    <Icon name="alert" size={12} /> {errs[`k.${i}`]}
                  </div>
                )}
                {!isMasked(k.key) && k.key && !k.key.startsWith("env:") && (
                  <div className="small faint" style={{ marginTop: 4 }}>
                    {t("set.copyNow")}
                  </div>
                )}
              </div>
            ))}
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>{t("set.adminPw")}</h2>
          </div>
          <div className="card-body stack">
            <p className="muted small">{t("set.adminPwHint")}</p>
            <div className="row wrap">
              {draft.admin_password ? (
                <>
                  <Badge tone="ok" icon="lock">
                    {isMasked(draft.admin_password) ? t("set.pwSet", { hint: maskHint(draft.admin_password) }) : draft.admin_password.startsWith("env:") ? draft.admin_password : t("set.pwNew")}
                  </Badge>
                  <button className="btn sm danger" onClick={() => set("admin_password", "")}>
                    {t("set.pwRemove")}
                  </button>
                </>
              ) : (
                <Badge tone="warn" icon="alert">
                  {t("set.pwNone")}
                </Badge>
              )}
            </div>
            <div className="row wrap">
              <input className="input mono" type="password" autoComplete="new-password" style={{ maxWidth: 280 }} aria-label={t("set.pwNewLabel")} placeholder={t("set.pwNewLabel")} value={newPw} onChange={(e) => setNewPw(e.target.value)} />
              <button className="btn sm" disabled={!newPw} onClick={() => set("admin_password", newPw)}>
                {t("set.pwApply")}
              </button>
            </div>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>{t("set.timeouts")}</h2>
          </div>
          <div className="card-body grid-3">
            {TIMEOUTS.map((k) => (
              <Field key={k} label={k} hint={t(timeoutHelp[k])} error={errs[`t.${k}`]}>
                {(id, d) => (
                  <input id={id} aria-describedby={d} aria-invalid={!!errs[`t.${k}`]} className="input mono" value={draft.timeouts[k]} onChange={(e) => set("timeouts", { ...draft.timeouts, [k]: e.target.value })} placeholder="90s" />
                )}
              </Field>
            ))}
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>{t("set.routing")}</h2>
          </div>
          <div className="card-body grid-2">
            <Field label="strategy" hint={t(`set.strat.${draft.routing.strategy}` as Key) || ""}>
              {(id, d) => (
                <select id={id} aria-describedby={d} className="input" value={draft.routing.strategy} onChange={(e) => set("routing", { ...draft.routing, strategy: e.target.value })}>
                  <option value="round-robin">round-robin</option>
                  <option value="fill-first">fill-first</option>
                  <option value="least-busy">least-busy</option>
                </select>
              )}
            </Field>
            <Field label="max_attempts" hint={t("set.maxAttemptsHint")} error={errs.max_attempts}>
              {(id, d) => <input id={id} aria-describedby={d} className="input num" inputMode="numeric" value={draft.routing.max_attempts} onChange={(e) => set("routing", { ...draft.routing, max_attempts: e.target.value })} />}
            </Field>
            <Field label="max_body_mb" hint={t("set.maxBodyHint")} error={errs.max_body_mb}>
              {(id, d) => <input id={id} aria-describedby={d} className="input num" inputMode="numeric" value={draft.routing.max_body_mb} onChange={(e) => set("routing", { ...draft.routing, max_body_mb: e.target.value })} />}
            </Field>
            <div className="field" style={{ justifyContent: "center" }}>
              <Switch checked={draft.routing.session_affinity} onChange={(v) => set("routing", { ...draft.routing, session_affinity: v })} label="session_affinity" />
              <span className="hint">{t("set.affinityHint")}</span>
            </div>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>{t("set.logging")}</h2>
          </div>
          <div className="card-body grid-3">
            <Field label="level">
              {(id) => (
                <select id={id} className="input" value={draft.logging.level} onChange={(e) => set("logging", { ...draft.logging, level: e.target.value })}>
                  {["debug", "info", "warn", "error"].map((l) => (
                    <option key={l} value={l}>
                      {l}
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <Field label="retention_days" hint={t("set.retentionHint")} error={errs.retention_days}>
              {(id, d) => <input id={id} aria-describedby={d} className="input num" inputMode="numeric" value={draft.logging.retention_days} onChange={(e) => set("logging", { ...draft.logging, retention_days: e.target.value })} />}
            </Field>
            <div className="field" style={{ justifyContent: "center" }}>
              <Switch checked={draft.logging.capture_bodies} onChange={(v) => set("logging", { ...draft.logging, capture_bodies: v })} label="capture_bodies" />
              <span className="hint">{t("set.captureHint")}</span>
            </div>
          </div>
        </section>
      </div>

      {(changes.length > 0 || serverErr) && (
        <div className="savebar" role="region" aria-label={t("save.unsaved")}>
          <div className="grow" style={{ minWidth: 200 }}>
            <div style={{ fontWeight: 600 }}>{t("save.unsavedN", { n: changes.length })}</div>
            <div className="faint small mono ellipsis" title={changes.join("\n")}>
              {changes.slice(0, 4).join(" · ")}
            </div>
            {nErr > 0 && (
              <div className="small" style={{ color: "var(--err)" }}>
                <Icon name="alert" size={12} /> {t("val.fixErrors", { n: nErr })}
              </div>
            )}
            {serverErr && (
              <div className="small" style={{ color: "var(--err)", whiteSpace: "pre-wrap" }}>
                <Icon name="alert" size={12} /> {serverErr}
              </div>
            )}
          </div>
          <button
            className="btn"
            disabled={busy}
            onClick={() => {
              setDraft(toDraft(config));
              setServerErr(undefined);
              setNewPw("");
            }}
          >
            {t("save.discard")}
          </button>
          <button className="btn primary" onClick={submit} disabled={busy || nErr > 0}>
            {busy ? t("common.saving") : t("common.save")}
          </button>
        </div>
      )}
    </div>
  );
}
