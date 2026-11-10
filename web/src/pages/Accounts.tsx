import { Fragment, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { useNow, usePoll } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import type { AccountStatus } from "../types";
import { Badge, Empty, ErrorNote, useConfirm, useToast } from "../ui";
import OAuthAccounts from "./OAuthAccounts";
import { fmtAgo, fmtCountdown, fmtDateTime, fmtNum, fmtTime } from "../util";

type Health = "ok" | "cooling" | "error";

function health(a: AccountStatus, now: number): Health {
  if (a.cooldown_until && new Date(a.cooldown_until).getTime() > now) return "cooling";
  if (a.last_error && a.last_error_at && a.last_used && new Date(a.last_error_at).getTime() >= new Date(a.last_used).getTime()) return "error";
  return "ok";
}

export default function Accounts() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const { data, error, loading, refresh } = usePoll((s) => api.accounts(s), 3000);
  const now = useNow(1000);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState<string>();

  const groups = useMemo(() => {
    const m = new Map<string, AccountStatus[]>();
    for (const a of data || []) {
      if (!m.has(a.provider)) m.set(a.provider, []);
      m.get(a.provider)!.push(a);
    }
    return [...m.entries()];
  }, [data]);

  const reset = async (a: AccountStatus) => {
    const ok = await confirm({ title: t("acc.resetTitle", { id: a.label || a.id }), body: t("acc.resetBody"), confirm: t("acc.reset") });
    if (!ok) return;
    setBusy(a.id);
    try {
      await api.resetAccount(a.id);
      toast({ kind: "ok", title: t("acc.resetOk", { id: a.label || a.id }) });
      refresh();
    } catch (e) {
      toast({ kind: "err", title: t("acc.resetFail"), body: (e as Error).message });
    } finally {
      setBusy(undefined);
    }
  };

  const toggle = (id: string) =>
    setOpen((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  const counts = useMemo(() => {
    const c = { ok: 0, cooling: 0, error: 0 };
    for (const a of data || []) c[health(a, now)]++;
    return c;
  }, [data, now]);

  return (
    <div>
      <div className="page-head">
        <div className="grow">
          <h1>{t("nav.accounts")}</h1>
          <div className="desc">{t("acc.desc")}</div>
        </div>
        {data && data.length > 0 && (
          <div className="row wrap">
            <Badge tone="ok" icon="check">
              {counts.ok} {t("acc.healthy")}
            </Badge>
            <Badge tone="warn" icon="clock">
              {counts.cooling} {t("acc.coolingLc")}
            </Badge>
            <Badge tone="err" icon="alert">
              {counts.error} {t("acc.errorLc")}
            </Badge>
          </div>
        )}
      </div>
      <OAuthAccounts />
      {error && <div style={{ marginBottom: 12 }}><ErrorNote error={error} /></div>}
      {loading && !data && <div className="faint">{t("common.loading")}</div>}
      {data && data.length === 0 && (
        <div className="card">
          <Empty
            icon="accounts"
            title={t("acc.emptyTitle")}
            body={t("acc.emptyBody")}
            action={
              <Link className="btn primary" to="/providers">
                {t("acc.goProviders")}
              </Link>
            }
          />
        </div>
      )}
      <div className="stack">
        {groups.map(([prov, accs]) => (
          <div className="card" key={prov}>
            <div className="card-head">
              <h2 className="mono">{prov}</h2>
              <span className="faint small">
                {accs.length} {t("acc.accountsLc")}
              </span>
            </div>
            <div className="table-wrap">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>{t("acc.account")}</th>
                    <th>{t("acc.health")}</th>
                    <th className="r">{t("acc.inflight")}</th>
                    <th className="r">{t("acc.okFail")}</th>
                    <th className="hide-sm">{t("acc.lastUsed")}</th>
                    <th>{t("acc.lastError")}</th>
                    <th className="r">
                      <span className="sr-only">{t("common.actions")}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {accs.map((a) => {
                    const h = health(a, now);
                    const mc = Object.entries(a.model_cooldown || {}).filter(([, u]) => new Date(u).getTime() > now);
                    const isOpen = open.has(a.id);
                    return (
                      <Fragment key={a.id}>
                        <tr>
                          <td style={{ maxWidth: 240 }}>
                            <div className="mono ellipsis" title={a.id}>
                              {a.label || a.id}
                            </div>
                            <div className="faint small row" style={{ gap: 6 }}>
                              <span>{a.kind === "oauth" ? "OAuth" : "API key"}</span>
                              {a.meta &&
                                Object.entries(a.meta)
                                  .slice(0, 2)
                                  .map(([k, v]) => (
                                    <span key={k} className="ellipsis" title={`${k}: ${v}`}>
                                      · {v}
                                    </span>
                                  ))}
                            </div>
                          </td>
                          <td>
                            <div className="col" style={{ gap: 4, alignItems: "flex-start" }}>
                              {h === "cooling" ? (
                                <Badge tone="warn" icon="clock" title={fmtDateTime(a.cooldown_until!)}>
                                  {t("acc.coolingUntil", { time: fmtTime(a.cooldown_until!), left: fmtCountdown(new Date(a.cooldown_until!).getTime() - now) })}
                                </Badge>
                              ) : h === "error" ? (
                                <Badge tone="err" icon="alert">
                                  {t("acc.error")}
                                </Badge>
                              ) : (
                                <Badge tone="ok" icon="check">
                                  {t("acc.healthyCap")}
                                </Badge>
                              )}
                              {mc.map(([m, u]) => (
                                <Badge key={m} tone="warn" icon="clock" title={fmtDateTime(u)}>
                                  <span className="mono">{m}</span> · {fmtCountdown(new Date(u).getTime() - now)}
                                </Badge>
                              ))}
                            </div>
                          </td>
                          <td className="r">{a.inflight}</td>
                          <td className="r nowrap">
                            <span style={{ color: "var(--ok)" }}>{fmtNum(a.successes)}</span>
                            <span className="faint"> / </span>
                            <span style={{ color: a.failures ? "var(--err)" : undefined }}>{fmtNum(a.failures)}</span>
                          </td>
                          <td className="hide-sm nowrap faint" title={a.last_used ? fmtDateTime(a.last_used) : undefined}>
                            {a.last_used ? t("acc.ago", { t: fmtAgo(a.last_used, now) }) : "–"}
                          </td>
                          <td style={{ maxWidth: 320 }}>
                            {a.last_error ? (
                              <button className="btn ghost sm" style={{ maxWidth: "100%", justifyContent: "flex-start" }} aria-expanded={isOpen} onClick={() => toggle(a.id)}>
                                <Icon name={isOpen ? "down" : "right"} size={12} />
                                <span className="ellipsis">{a.last_error}</span>
                              </button>
                            ) : (
                              <span className="faint">–</span>
                            )}
                          </td>
                          <td className="r">
                            <button className="btn sm" onClick={() => reset(a)} disabled={busy === a.id} aria-label={`${t("acc.reset")} ${a.label || a.id}`}>
                              <Icon name="refresh" size={13} /> {t("acc.reset")}
                            </button>
                          </td>
                        </tr>
                        {isOpen && a.last_error && (
                          <tr>
                            <td colSpan={7} style={{ background: "var(--surface-2)" }}>
                              <div className="faint small" style={{ marginBottom: 4 }}>
                                {a.last_error_at && fmtDateTime(a.last_error_at)}
                              </div>
                              <pre className="mono" style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-word", color: "var(--err)" }}>
                                {a.last_error}
                              </pre>
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
