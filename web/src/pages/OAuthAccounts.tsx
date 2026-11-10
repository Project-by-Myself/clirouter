import { useState } from "react";
import { api, ApiError } from "../api";
import { useNow, usePoll } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { attachAccount, OAUTH_LABEL, OAuthFlow, useSubscriptionConsent } from "../oauth";
import { useConfig } from "../store";
import { OAUTH_PROVIDER_TYPE, type OAuthAccount } from "../types";
import { Badge, ErrorNote, useConfirm, useToast } from "../ui";
import { clone, fmtAgo, fmtCountdown, fmtDateTime } from "../util";

const TYPES = ["claude", "codex", "copilot", "gemini-cli"];

export default function OAuthAccounts() {
  const consent = useSubscriptionConsent();
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const { config, save } = useConfig();
  const now = useNow(1000);
  const { data, error, refresh } = usePoll((s) => api.oauthAccounts(s), 10000);
  const [type, setType] = useState("claude");
  const [target, setTarget] = useState("");
  const [login, setLogin] = useState(false);

  const disabled = error instanceof ApiError && error.status === 404;
  const ptype = OAUTH_PROVIDER_TYPE[type];
  const candidates = (config?.providers || []).filter((p) => p.type === ptype);
  const usedBy = (id: string) => (config?.providers || []).filter((p) => p.oauth.includes(id)).map((p) => p.id);

  const onDone = async (accountId: string) => {
    setLogin(false);
    refresh();
    if (!config) return;
    const { next, providerId, created } = attachAccount(config, type, accountId, target || undefined);
    const r = await save(next);
    if (r.ok) toast({ kind: "ok", title: t("oauth.done", { id: accountId }), body: created ? t("oauth.createdProvider", { p: providerId }) : t("oauth.attached", { p: providerId }) });
  };

  const remove = async (a: OAuthAccount) => {
    const refs = usedBy(a.account_id);
    const ok = await confirm({
      title: t("oauth.deleteTitle", { id: a.account_id }),
      body: refs.length ? t("oauth.deleteBodyRefs", { p: refs.join(", ") }) : t("oauth.deleteBody"),
      confirm: t("common.delete"),
      danger: true,
    });
    if (!ok) return;
    try {
      await api.oauthDelete(a.account_id);
      toast({ kind: "ok", title: t("oauth.deleted", { id: a.account_id }) });
      refresh();
      if (refs.length && config) {
        const next = clone(config);
        next.providers = next.providers.map((p) => ({ ...p, oauth: p.oauth.filter((x) => x !== a.account_id) }));
        await save(next);
      }
    } catch (e) {
      toast({ kind: "err", title: t("oauth.deleteFail"), body: (e as Error).message });
    }
  };

  return (
    <section className="card" aria-labelledby="oauth-h">
      <div className="card-head" style={{ flexWrap: "wrap" }}>
        <h2 id="oauth-h" className="grow">
          {t("oauth.title")}
        </h2>
        {!disabled && !login && (
          <div className="row wrap">
            <select className="input" style={{ width: "auto" }} aria-label={t("oauth.type")} value={type} onChange={(e) => { setType(e.target.value); setTarget(""); }}>
              {TYPES.map((x) => (
                <option key={x} value={x}>
                  {OAUTH_LABEL[x]}
                </option>
              ))}
            </select>
            <select className="input" style={{ width: "auto" }} aria-label={t("oauth.attachTo")} value={target} onChange={(e) => setTarget(e.target.value)}>
              {candidates.length === 0 && <option value="">{t("oauth.newProvider", { t: ptype })}</option>}
              {candidates.map((p, i) => (
                <option key={p.id} value={i === 0 ? "" : p.id}>
                  → {p.id}
                </option>
              ))}
            </select>
            <button className="btn primary sm" onClick={async () => (await consent(type)) && setLogin(true)}>
              <Icon name="lock" size={13} /> {t("oauth.login")}
            </button>
          </div>
        )}
      </div>
      {!disabled && !login && (
        <div className="oauth-note small" role="note">
          <Icon name="alert" size={13} style={{ flex: "none", marginTop: 2 }} />
          <span>{t("oauth.persistNote")}</span>
        </div>
      )}
      {login && (
        <div className="card-body">
          <OAuthFlow oauthType={type} onDone={onDone} onCancel={() => setLogin(false)} />
        </div>
      )}
      {disabled ? (
        <div className="card-body faint small">{t("oauth.disabled")}</div>
      ) : error && !data ? (
        <div className="card-body">
          <ErrorNote error={error} />
        </div>
      ) : data && data.length === 0 ? (
        !login && (
          <div className="card-body row" style={{ gap: 12 }}>
            <Icon name="key" size={16} className="faint" />
            <div className="grow small">
              <strong>{t("oauth.emptyTitle")}</strong> <span className="muted">— {t("oauth.emptyBody")}</span>
            </div>
          </div>
        )
      ) : data ? (
        <div className="table-wrap">
          <table className="tbl">
            <thead>
              <tr>
                <th>{t("acc.account")}</th>
                <th>{t("oauth.type")}</th>
                <th className="hide-sm">{t("oauth.plan")}</th>
                <th>{t("oauth.token")}</th>
                <th className="hide-sm">{t("oauth.usedBy")}</th>
                <th className="r">
                  <span className="sr-only">{t("common.actions")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {data.map((a) => {
                const refs = usedBy(a.account_id);
                const exp = a.expires_at ? new Date(a.expires_at).getTime() - now : undefined;
                return (
                  <tr key={a.account_id}>
                    <td style={{ maxWidth: 260 }}>
                      <div className="mono ellipsis">{a.account_id}</div>
                      {a.email && <div className="faint small ellipsis">{a.email}</div>}
                    </td>
                    <td className="nowrap">{OAUTH_LABEL[a.type] || a.type}</td>
                    <td className="hide-sm">{a.plan || <span className="faint">–</span>}</td>
                    <td>
                      <div className="col" style={{ gap: 4, alignItems: "flex-start" }}>
                        {a.last_error ? (
                          <Badge tone="err" icon="alert" title={a.last_error}>
                            {t("acc.error")}
                          </Badge>
                        ) : exp === undefined ? (
                          <Badge tone="ok" icon="check">
                            OK
                          </Badge>
                        ) : exp > 0 ? (
                          <Badge tone="ok" icon="check" title={fmtDateTime(a.expires_at!)}>
                            {t("oauth.expiresIn", { t: fmtCountdown(exp) })}
                          </Badge>
                        ) : (
                          <Badge tone="warn" icon="clock" title={fmtDateTime(a.expires_at!)}>
                            {t("oauth.expired")}
                          </Badge>
                        )}
                        {a.last_refresh && <span className="faint small">{t("oauth.refreshed", { t: fmtAgo(a.last_refresh, now) })}</span>}
                        {a.last_error && <span className="small ellipsis" style={{ color: "var(--err)", maxWidth: 260 }} title={a.last_error}>{a.last_error}</span>}
                      </div>
                    </td>
                    <td className="hide-sm mono small">{refs.length ? refs.join(", ") : <Badge tone="warn" icon="alert">{t("oauth.unused")}</Badge>}</td>
                    <td className="r">
                      <button className="btn sm ghost danger" onClick={() => remove(a)} aria-label={t("oauth.deleteTitle", { id: a.account_id })}>
                        <Icon name="trash" size={13} />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="card-body faint">{t("common.loading")}</div>
      )}
    </section>
  );
}
