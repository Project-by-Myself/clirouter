import { useEffect, useMemo, useState } from "react";
import { Autocomplete, type Suggestion } from "../editors";
import { useT, type TFn } from "../i18n";
import { Icon } from "../icons";
import { diffConfig, useConfig } from "../store";
import type { Combo, Config } from "../types";
import { Empty, ErrorNote, useConfirm } from "../ui";
import { clone } from "../util";

interface Draft {
  combos: Combo[];
  aliases: [string, string][];
}

function toDraft(c: Config): Draft {
  return { combos: clone(c.combos), aliases: Object.entries(c.aliases) };
}

function apply(c: Config, d: Draft): Config {
  const next = clone(c);
  next.combos = d.combos.map((x) => ({ name: x.name.trim(), strategy: x.strategy, targets: x.targets.map((s) => s.trim()).filter(Boolean) }));
  next.aliases = {};
  for (const [k, v] of d.aliases) if (k.trim()) next.aliases[k.trim()] = v.trim();
  return next;
}

function validate(d: Draft, c: Config, t: TFn): string[] {
  const errs: string[] = [];
  const pids = new Set(c.providers.map((p) => p.id));
  const names = new Set<string>();
  d.combos.forEach((x, i) => {
    const n = x.name.trim();
    if (!n) errs.push(t("combo.errName", { i: i + 1 }));
    else if (names.has(n)) errs.push(t("combo.errDup", { n }));
    else if (pids.has(n)) errs.push(t("combo.errProv", { n }));
    names.add(n);
    if (!x.targets.some((s) => s.trim())) errs.push(t("combo.errTargets", { n: n || String(i + 1) }));
  });
  const an = new Set<string>();
  d.aliases.forEach(([k, v]) => {
    const kk = k.trim();
    if (!kk && !v.trim()) return;
    if (!kk) errs.push(t("alias.errName"));
    else if (an.has(kk)) errs.push(t("alias.errDup", { n: kk }));
    else if (!v.trim()) errs.push(t("alias.errTarget", { n: kk }));
    an.add(kk);
  });
  return errs;
}

export default function Combos() {
  const t = useT();
  const confirm = useConfirm();
  const { config, error, save, models } = useConfig();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [busy, setBusy] = useState(false);
  const [serverErr, setServerErr] = useState<string>();
  const [newTarget, setNewTarget] = useState<Record<number, string>>({});

  useEffect(() => {
    if (config && !draft) setDraft(toDraft(config));
  }, [config, draft]);

  const suggestions = useMemo<Suggestion[]>(() => {
    const s: Suggestion[] = models.map((m) => ({ value: m.id, hint: m.owner }));
    for (const p of config?.providers || []) s.push({ value: `${p.id}/`, hint: t("combo.provPrefix") });
    return s;
  }, [models, config, t]);

  if (!config || !draft) return error ? <ErrorNote error={error} /> : <div className="faint">{t("common.loading")}</div>;

  const next = apply(config, draft);
  const changes = diffConfig(config, next, t);
  const dirty = changes.length > 0;
  const errs = validate(draft, config, t);

  const setCombo = (i: number, c: Partial<Combo>) => setDraft({ ...draft, combos: draft.combos.map((x, j) => (j === i ? { ...x, ...c } : x)) });
  const move = (i: number, ti: number, dir: -1 | 1) => {
    const ts = [...draft.combos[i].targets];
    const j = ti + dir;
    if (j < 0 || j >= ts.length) return;
    [ts[ti], ts[j]] = [ts[j], ts[ti]];
    setCombo(i, { targets: ts });
  };
  const addTarget = (i: number) => {
    const v = (newTarget[i] || "").trim();
    if (!v || v.endsWith("/")) return;
    setCombo(i, { targets: [...draft.combos[i].targets, v] });
    setNewTarget({ ...newTarget, [i]: "" });
  };
  const removeCombo = async (i: number) => {
    const c = draft.combos[i];
    if (c.name && !(await confirm({ title: t("combo.deleteTitle", { n: c.name }), confirm: t("common.delete"), danger: true }))) return;
    setDraft({ ...draft, combos: draft.combos.filter((_, j) => j !== i) });
  };

  const submit = async () => {
    if (errs.length) return;
    setBusy(true);
    setServerErr(undefined);
    const r = await save(next);
    setBusy(false);
    if (r.ok) setDraft(null);
    else setServerErr(r.error);
  };

  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.combos")}</h1>
          <div className="desc">{t("combo.desc")}</div>
        </div>
      </div>

      <div className="stack-lg">
        <section className="card" aria-labelledby="combos-h">
          <div className="card-head">
            <h2 id="combos-h" className="grow">
              Combos
            </h2>
            <button className="btn sm" onClick={() => setDraft({ ...draft, combos: [...draft.combos, { name: "", strategy: "fallback", targets: [] }] })}>
              <Icon name="plus" size={13} /> {t("combo.add")}
            </button>
          </div>
          {draft.combos.length === 0 ? (
            <Empty icon="combos" title={t("combo.emptyTitle")} body={t("combo.emptyBody")} />
          ) : (
            <div className="card-body stack">
              {draft.combos.map((c, i) => (
                <div key={i} className="card card-pad" style={{ background: "var(--surface-2)" }}>
                  <div className="row wrap" style={{ alignItems: "flex-end" }}>
                    <div className="field grow" style={{ minWidth: 160 }}>
                      <label htmlFor={`combo-name-${i}`}>{t("common.name")}</label>
                      <input id={`combo-name-${i}`} className="input mono" value={c.name} placeholder="smart" onChange={(e) => setCombo(i, { name: e.target.value })} />
                    </div>
                    <div className="field" style={{ width: 170 }}>
                      <label htmlFor={`combo-strat-${i}`}>{t("combo.strategy")}</label>
                      <select id={`combo-strat-${i}`} className="input" value={c.strategy || "fallback"} onChange={(e) => setCombo(i, { strategy: e.target.value })}>
                        <option value="fallback">fallback</option>
                        <option value="round-robin">round-robin</option>
                      </select>
                    </div>
                    <button className="btn ghost danger" onClick={() => removeCombo(i)} aria-label={t("combo.deleteTitle", { n: c.name || String(i + 1) })}>
                      <Icon name="trash" size={14} />
                    </button>
                  </div>
                  <div className="faint small" style={{ margin: "10px 0 6px" }}>
                    {c.strategy === "round-robin" ? t("combo.rrHint") : t("combo.fbHint")}
                  </div>
                  <ol style={{ listStyle: "none", padding: 0, margin: 0 }} aria-label={t("combo.targets")}>
                    {c.targets.map((tg, ti) => (
                      <li key={`${tg}-${ti}`} className="list-row">
                        <span className="faint num small" style={{ width: 18 }}>
                          {ti + 1}.
                        </span>
                        <span className="mono grow ellipsis">{tg}</span>
                        <button className="btn ghost icon sm" disabled={ti === 0} onClick={() => move(i, ti, -1)} aria-label={t("combo.moveUp", { x: tg })}>
                          <Icon name="up" size={14} />
                        </button>
                        <button className="btn ghost icon sm" disabled={ti === c.targets.length - 1} onClick={() => move(i, ti, 1)} aria-label={t("combo.moveDown", { x: tg })}>
                          <Icon name="down" size={14} />
                        </button>
                        <button className="btn ghost icon sm" onClick={() => setCombo(i, { targets: c.targets.filter((_, j) => j !== ti) })} aria-label={t("common.removeX", { x: tg })}>
                          <Icon name="x" size={14} />
                        </button>
                      </li>
                    ))}
                  </ol>
                  <div className="row" style={{ marginTop: 6 }}>
                    <Autocomplete
                      label={t("combo.addTarget")}
                      value={newTarget[i] || ""}
                      onChange={(v) => setNewTarget({ ...newTarget, [i]: v })}
                      suggestions={suggestions}
                      placeholder={t("combo.targetPh")}
                      onEnter={() => addTarget(i)}
                    />
                    <button className="btn" onClick={() => addTarget(i)} disabled={!(newTarget[i] || "").trim()}>
                      <Icon name="plus" size={13} /> {t("combo.addTarget")}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        <section className="card" aria-labelledby="aliases-h">
          <div className="card-head">
            <h2 id="aliases-h" className="grow">
              Aliases
            </h2>
            <button className="btn sm" onClick={() => setDraft({ ...draft, aliases: [...draft.aliases, ["", ""]] })}>
              <Icon name="plus" size={13} /> {t("alias.add")}
            </button>
          </div>
          {draft.aliases.length === 0 ? (
            <Empty icon="combos" title={t("alias.emptyTitle")} body={t("alias.emptyBody")} />
          ) : (
            <div className="card-body">
              <div className="faint small" style={{ marginBottom: 8 }}>
                {t("alias.hint")}
              </div>
              {draft.aliases.map(([k, v], i) => (
                <div key={i} className="row" style={{ marginBottom: 6, alignItems: "stretch" }}>
                  <input
                    className="input mono"
                    style={{ maxWidth: 220 }}
                    aria-label={t("alias.name", { i: i + 1 })}
                    placeholder="sonnet"
                    value={k}
                    onChange={(e) => setDraft({ ...draft, aliases: draft.aliases.map((r, j) => (j === i ? [e.target.value, r[1]] : r)) })}
                  />
                  <span className="faint" style={{ alignSelf: "center" }}>
                    →
                  </span>
                  <Autocomplete
                    label={t("alias.target", { i: i + 1 })}
                    value={v}
                    onChange={(nv) => setDraft({ ...draft, aliases: draft.aliases.map((r, j) => (j === i ? [r[0], nv] : r)) })}
                    suggestions={suggestions}
                    placeholder="anthropic/claude-sonnet-4-5"
                  />
                  <button className="btn ghost icon" aria-label={t("common.removeX", { x: k || String(i + 1) })} onClick={() => setDraft({ ...draft, aliases: draft.aliases.filter((_, j) => j !== i) })}>
                    <Icon name="trash" size={14} />
                  </button>
                </div>
              ))}
            </div>
          )}
        </section>
      </div>

      {(dirty || serverErr) && (
        <div className="savebar" role="region" aria-label={t("save.unsaved")}>
          <div className="grow" style={{ minWidth: 200 }}>
            <div style={{ fontWeight: 600 }}>{t("save.unsavedN", { n: changes.length })}</div>
            <div className="faint small mono ellipsis" title={changes.join("\n")}>
              {changes.slice(0, 4).join(" · ")}
            </div>
            {errs.length > 0 && (
              <div className="small" style={{ color: "var(--err)" }}>
                <Icon name="alert" size={12} /> {errs.join(" · ")}
              </div>
            )}
            {serverErr && (
              <div className="small" style={{ color: "var(--err)", whiteSpace: "pre-wrap" }}>
                <Icon name="alert" size={12} /> {serverErr}
              </div>
            )}
          </div>
          <button className="btn" onClick={() => { setDraft(toDraft(config)); setServerErr(undefined); }} disabled={busy}>
            {t("save.discard")}
          </button>
          <button className="btn primary" onClick={submit} disabled={busy || errs.length > 0}>
            {busy ? t("common.saving") : t("common.save")}
          </button>
        </div>
      )}
    </div>
  );
}
